package auth

import (
	"context"
	"errors"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/sms"
)

const testPhone = "+233241234567"

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newOTP(t *testing.T, dailyCap int) (*OTP, *sms.Capture, *clock) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	require.NoError(t, db.Migrate(dsn))
	d, err := db.Open(ctx, dsn, 4)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	_, err = d.SQL.ExecContext(ctx, "TRUNCATE notifications, reports, messages, conversations, viewings, saved_listings, agent_mandates, listing_media, listing_terms, listings, units, properties, audit_events, sessions, role_assignments, otp_codes, verification_files, verifications, agent_profiles, landlord_profiles, users")
	require.NoError(t, err)

	capture := &sms.Capture{}
	o := NewOTP(d.Ent, capture, audit.New(d.Ent), "test-secret-test-secret-test-secret", dailyCap, "")
	c := &clock{t: time.Now().UTC().Truncate(time.Second)}
	o.now = c.now
	return o, capture, c
}

var digits = regexp.MustCompile(`\d{6}`)

func lastCode(c *sms.Capture) string { return digits.FindString(c.Last().Body) }

func TestOTPWrongGuessesBurnTheCode(t *testing.T) {
	o, capture, _ := newOTP(t, 100)
	ctx := context.Background()
	_, err := o.Request(ctx, testPhone, "ip")
	require.NoError(t, err)
	code := lastCode(capture)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}

	for i := 1; i < MaxAttempts; i++ {
		var w WrongCodeError
		require.ErrorAs(t, o.Verify(ctx, testPhone, wrong, "ip"), &w)
		assert.Equal(t, MaxAttempts-i, w.Remaining)
	}
	assert.ErrorIs(t, o.Verify(ctx, testPhone, wrong, "ip"), ErrTooManyAttempts)
	// Even the right code is useless now.
	assert.ErrorIs(t, o.Verify(ctx, testPhone, code, "ip"), ErrCodeExpired)
}

func TestOTPExpiresAndSupersedes(t *testing.T) {
	o, capture, clk := newOTP(t, 100)
	ctx := context.Background()

	_, err := o.Request(ctx, testPhone, "ip")
	require.NoError(t, err)
	first := lastCode(capture)

	clk.advance(ResendCooldown + time.Second)
	_, err = o.Request(ctx, testPhone, "ip")
	require.NoError(t, err)
	second := lastCode(capture)
	if first != second {
		assert.Error(t, o.Verify(ctx, testPhone, first, "ip"), "older code superseded")
	}

	clk.advance(CodeTTL + time.Second)
	assert.ErrorIs(t, o.Verify(ctx, testPhone, second, "ip"), ErrCodeExpired)
}

func TestOTPMalformedInputDoesNotCostAnAttempt(t *testing.T) {
	o, capture, _ := newOTP(t, 100)
	ctx := context.Background()
	_, err := o.Request(ctx, testPhone, "ip")
	require.NoError(t, err)
	for _, in := range []string{"", "12345", "12a456", "1234567"} {
		assert.ErrorIs(t, o.Verify(ctx, testPhone, in, "ip"), ErrCodeMalformed, in)
	}
	assert.NoError(t, o.Verify(ctx, testPhone, lastCode(capture), "ip"))
}

func TestOTPCooldownAndPerPhoneLimit(t *testing.T) {
	o, _, clk := newOTP(t, 100)
	ctx := context.Background()

	_, err := o.Request(ctx, testPhone, "ip")
	require.NoError(t, err)
	var cd CooldownError
	require.ErrorAs(t, func() error { _, err := o.Request(ctx, testPhone, "ip"); return err }(), &cd)
	assert.InDelta(t, ResendCooldown.Seconds(), cd.Wait.Seconds(), 1)

	for i := 1; i < MaxPerHour; i++ {
		clk.advance(ResendCooldown)
		_, err := o.Request(ctx, testPhone, "ip")
		require.NoError(t, err, "send %d", i+1)
	}
	clk.advance(ResendCooldown)
	_, err = o.Request(ctx, testPhone, "ip")
	assert.ErrorIs(t, err, ErrTooManyCodes)

	// Another number is unaffected.
	_, err = o.Request(ctx, "+233501234567", "ip")
	assert.NoError(t, err)
}

func TestOTPGlobalDailyCap(t *testing.T) {
	o, _, _ := newOTP(t, 2)
	ctx := context.Background()
	for _, p := range []string{"+233241111111", "+233242222222"} {
		_, err := o.Request(ctx, p, "ip")
		require.NoError(t, err)
	}
	_, err := o.Request(ctx, "+233243333333", "ip")
	assert.ErrorIs(t, err, ErrDailyCap)
}

func TestOTPFailedSendDoesNotLockTheUserOut(t *testing.T) {
	o, capture, _ := newOTP(t, 100)
	ctx := context.Background()
	capture.Err = errors.New("provider down")
	_, err := o.Request(ctx, testPhone, "ip")
	assert.ErrorIs(t, err, ErrSendFailed)

	capture.Err = nil
	_, err = o.Request(ctx, testPhone, "ip")
	assert.NoError(t, err, "no cooldown after an undelivered code")
}

func TestPendingCookieRejectsTampering(t *testing.T) {
	c := cookies{secret: []byte("s")}
	now := time.Now()
	rec := newRecorder()
	c.setPending(rec, testPhone, now)
	req := requestWith(rec)
	assert.Equal(t, testPhone, c.pending(req, now))
	assert.Empty(t, c.pending(req, now.Add(pendingTTL+time.Second)), "expired")
	assert.Empty(t, cookies{secret: []byte("other")}.pending(req, now), "wrong key")
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/account":             "/account",
		"/listings?x=1":        "/listings?x=1",
		"":                     "",
		"https://evil.example": "",
		"//evil.example":       "",
		`/\evil.example`:       "",
		"account":              "",
		"/a\r\nSet-Cookie: x":  "",
	} {
		assert.Equal(t, want, SafeNext(in), in)
	}
}
