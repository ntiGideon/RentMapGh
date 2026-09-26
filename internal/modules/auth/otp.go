// Package auth implements phone-OTP sign-in, sessions and the auth middleware.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/otpcode"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/platform/sms"
)

// OTP policy (ProjectRequirement §6.6 and risk #2, SMS pumping).
const (
	CodeLength     = 6
	CodeTTL        = 5 * time.Minute
	ResendCooldown = 60 * time.Second
	MaxAttempts    = 5  // wrong guesses per code before it's burnt
	MaxPerHour     = 5  // codes per phone
	MaxPerDay      = 10 // codes per phone
)

// Errors carry user-facing copy.
var (
	ErrTooManyCodes    = errors.New("Too many codes requested for this number. Please try again in an hour.")
	ErrDailyCap        = errors.New("We can't send codes right now. Please try again later.")
	ErrSendFailed      = errors.New("We couldn't send the SMS. Check the number and try again.")
	ErrCodeMalformed   = errors.New("Enter the 6-digit code from the SMS.")
	ErrCodeExpired     = errors.New("That code has expired. Request a new one.")
	ErrTooManyAttempts = errors.New("Too many wrong attempts. Request a new code.")
)

// CooldownError means a code was sent moments ago.
type CooldownError struct{ Wait time.Duration }

func (e CooldownError) Error() string {
	return fmt.Sprintf("Please wait %d seconds before requesting another code.", int(e.Wait.Round(time.Second).Seconds()))
}

// WrongCodeError is a mismatch with guesses remaining.
type WrongCodeError struct{ Remaining int }

func (e WrongCodeError) Error() string {
	if e.Remaining == 1 {
		return "That code isn't right. You have 1 attempt left."
	}
	return fmt.Sprintf("That code isn't right. You have %d attempts left.", e.Remaining)
}

type OTP struct {
	db       *ent.Client
	sms      sms.Sender
	audit    *audit.Log
	secret   []byte
	dailyCap int
	host     string // for the WebOTP line, e.g. "rentmap.gh"
	now      func() time.Time
}

func NewOTP(db *ent.Client, sender sms.Sender, log *audit.Log, secret string, dailyCap int, host string) *OTP {
	return &OTP{db: db, sms: sender, audit: log, secret: []byte(secret), dailyCap: dailyCap, host: host,
		now: func() time.Time { return time.Now().UTC() }}
}

// Issued describes a code that was just sent.
type Issued struct {
	Phone     string
	ExpiresAt time.Time
	ResendAt  time.Time
}

// Request generates a code for e164 and sends it by SMS, enforcing the
// cooldown, per-phone and global limits.
func (o *OTP) Request(ctx context.Context, e164, ip string) (Issued, error) {
	now := o.now()

	recent, err := o.db.OTPCode.Query().
		Where(otpcode.Phone(e164), otpcode.CreatedAtGT(now.Add(-24*time.Hour))).
		Order(ent.Desc(otpcode.FieldCreatedAt)).
		Select(otpcode.FieldCreatedAt).
		All(ctx)
	if err != nil {
		return Issued{}, fmt.Errorf("otp: recent: %w", err)
	}
	if len(recent) > 0 {
		if wait := recent[0].CreatedAt.Add(ResendCooldown).Sub(now); wait > 0 {
			return Issued{}, CooldownError{Wait: wait}
		}
	}
	lastHour := 0
	for _, c := range recent {
		if c.CreatedAt.After(now.Add(-time.Hour)) {
			lastHour++
		}
	}
	if lastHour >= MaxPerHour || len(recent) >= MaxPerDay {
		o.audit.Record(ctx, audit.Event{Action: audit.OTPLocked, IP: ip, Meta: map[string]any{"phone": phone.Mask(e164), "reason": "per_phone_limit"}})
		return Issued{}, ErrTooManyCodes
	}

	dayStart := now.Truncate(24 * time.Hour)
	sentToday, err := o.db.OTPCode.Query().Where(otpcode.CreatedAtGTE(dayStart)).Count(ctx)
	if err != nil {
		return Issued{}, fmt.Errorf("otp: daily count: %w", err)
	}
	if sentToday >= o.dailyCap {
		slog.ErrorContext(ctx, "otp: global daily SMS cap reached — possible SMS pumping", "cap", o.dailyCap)
		return Issued{}, ErrDailyCap
	}

	code, err := randomCode()
	if err != nil {
		return Issued{}, err
	}
	tx, err := o.db.Tx(ctx)
	if err != nil {
		return Issued{}, fmt.Errorf("otp: begin: %w", err)
	}
	// A new code supersedes any older live one.
	if _, err := tx.OTPCode.Update().
		Where(otpcode.Phone(e164), otpcode.ConsumedAtIsNil()).
		SetConsumedAt(now).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return Issued{}, fmt.Errorf("otp: supersede: %w", err)
	}
	row, err := tx.OTPCode.Create().
		SetPhone(e164).
		SetCodeHash(o.hash(e164, code)).
		SetExpiresAt(now.Add(CodeTTL)).
		SetIP(ip).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return Issued{}, fmt.Errorf("otp: create: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Issued{}, fmt.Errorf("otp: commit: %w", err)
	}

	sendCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := o.sms.Send(sendCtx, sms.Message{To: e164, Body: o.message(code)}); err != nil {
		slog.ErrorContext(ctx, "otp: send", "provider", o.sms.Name(), "err", err)
		// Undelivered codes shouldn't block the user behind the cooldown.
		_ = o.db.OTPCode.DeleteOne(row).Exec(context.WithoutCancel(ctx))
		return Issued{}, ErrSendFailed
	}
	o.audit.Record(ctx, audit.Event{Action: audit.OTPSent, IP: ip, TargetType: "phone", TargetID: phone.Mask(e164),
		Meta: map[string]any{"provider": o.sms.Name()}})
	return Issued{Phone: e164, ExpiresAt: row.ExpiresAt, ResendAt: now.Add(ResendCooldown)}, nil
}

// Verify checks code against the live code for e164 and consumes it on success.
func (o *OTP) Verify(ctx context.Context, e164, input, ip string) error {
	code := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		if r == ' ' || r == '-' {
			return -1
		}
		return 'x'
	}, input)
	if len(code) != CodeLength || strings.ContainsRune(code, 'x') {
		return ErrCodeMalformed
	}
	now := o.now()

	live, err := o.db.OTPCode.Query().
		Where(otpcode.Phone(e164), otpcode.ConsumedAtIsNil(), otpcode.ExpiresAtGT(now)).
		Order(ent.Desc(otpcode.FieldCreatedAt)).
		First(ctx)
	if ent.IsNotFound(err) {
		return ErrCodeExpired
	}
	if err != nil {
		return fmt.Errorf("otp: load: %w", err)
	}

	// Count the attempt before comparing, atomically, so parallel guesses
	// can't exceed MaxAttempts.
	n, err := o.db.OTPCode.Update().
		Where(otpcode.ID(live.ID), otpcode.ConsumedAtIsNil(), otpcode.AttemptsLT(MaxAttempts)).
		AddAttempts(1).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("otp: count attempt: %w", err)
	}
	if n == 0 {
		return ErrTooManyAttempts
	}
	used := live.Attempts + 1

	if !hmac.Equal(live.CodeHash, o.hash(e164, code)) {
		o.audit.Record(ctx, audit.Event{Action: audit.OTPFailed, IP: ip, TargetType: "phone", TargetID: phone.Mask(e164),
			Meta: map[string]any{"attempt": used}})
		if used >= MaxAttempts {
			_ = o.db.OTPCode.UpdateOneID(live.ID).SetConsumedAt(now).Exec(ctx)
			o.audit.Record(ctx, audit.Event{Action: audit.OTPLocked, IP: ip, TargetType: "phone", TargetID: phone.Mask(e164),
				Meta: map[string]any{"reason": "max_attempts"}})
			return ErrTooManyAttempts
		}
		return WrongCodeError{Remaining: MaxAttempts - used}
	}

	n, err = o.db.OTPCode.Update().
		Where(otpcode.ID(live.ID), otpcode.ConsumedAtIsNil()).
		SetConsumedAt(now).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("otp: consume: %w", err)
	}
	if n == 0 { // a parallel request consumed it first
		return ErrCodeExpired
	}
	return nil
}

func (o *OTP) hash(e164, code string) []byte {
	m := hmac.New(sha256.New, o.secret)
	m.Write([]byte("otp:v1:" + e164 + ":" + code))
	return m.Sum(nil)
}

// message is the SMS text. The last line is the WebOTP format, which lets
// Android Chrome offer to fill the code in one tap.
func (o *OTP) message(code string) string {
	msg := "Your RentMap code is " + code + ". It expires in 5 minutes. Don't share it — RentMap will never ask for it."
	if o.host != "" {
		msg += "\n\n@" + o.host + " #" + code
	}
	return msg
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("otp: random: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
