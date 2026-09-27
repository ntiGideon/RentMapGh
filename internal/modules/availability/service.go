// Package availability keeps listings honest about being available
// (ProjectRequirement §6.7): listers confirm with one tap from an SMS, a
// renter can say "already rented", unconfirmed listings drop out of search,
// and marking a place rented asks the question that measures RentMap's
// success — did the tenant come through RentMap?
//
//	confirmed ── 3 days ──► ageing (nudge by SMS; again after 7 days)
//	          └─ 14 days ─► expired: hidden from search, lister told
//	             expired + 30 days ► archived (public page closes)
package availability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/report"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/listings"
	"rentmapgh/internal/modules/notify"
	"rentmapgh/internal/modules/viewings"
	"rentmapgh/internal/platform/sms"
)

const (
	FreshFor      = 3 * 24 * time.Hour
	ExpireAfter   = 14 * 24 * time.Hour
	ArchiveAfter  = 30 * 24 * time.Hour // after expiring
	RenudgeAfter  = 7 * 24 * time.Hour
	StaleNudgeGap = 12 * time.Hour // a renter's report nudges at most this often
	LinkTTL       = 10 * 24 * time.Hour
	QuietFrom     = 20 // no texts from 20:00…
	QuietUntil    = 7  // …to 07:00 (Ghana is GMT)
)

var (
	ErrBadLink  = errors.New("availability: link invalid or expired")
	ErrNotFound = errors.New("availability: not found")
)

// ValidationError maps field → message.
type ValidationError map[string]string

func (v ValidationError) Error() string {
	return fmt.Sprintf("availability: %d invalid field(s)", len(v))
}

// Actor is whoever acts (a signed-in user, or the lister holding a link).
type Actor struct {
	UserID        uuid.UUID
	IP, UserAgent string
}

type Service struct {
	notify   *notify.Service
	db       *ent.Client
	audit    *audit.Log
	sms      sms.Sender
	viewings *viewings.Service
	secret   []byte
	baseURL  string
	now      func() time.Time
}

// SetNotifier routes lister messages through the notification centre.
func (s *Service) SetNotifier(n *notify.Service) { s.notify = n }

func NewService(db *ent.Client, log *audit.Log, sender sms.Sender, v *viewings.Service, secret, baseURL string) *Service {
	return &Service{db: db, audit: log, sms: sender, viewings: v, secret: []byte(secret), baseURL: strings.TrimRight(baseURL, "/"),
		now: func() time.Time { return time.Now().UTC() }}
}

// ── Confirmation links ───────────────────────────────────────────────────
// Stateless and short (for SMS): listing ID + expiry, HMAC-signed. The link
// opens a page; nothing changes until the lister presses a button there
// (SMS apps and link previewers fetch links on their own).

func (s *Service) sign(payload []byte) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte("confirm:"))
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:9])
}

// Token makes a confirmation token for a listing.
func (s *Service) Token(id uuid.UUID) string {
	p := make([]byte, 20)
	copy(p, id[:])
	binary.BigEndian.PutUint32(p[16:], uint32(s.now().Add(LinkTTL).Unix())) //nolint:gosec // unix seconds fit until 2106
	return base64.RawURLEncoding.EncodeToString(p) + "." + s.sign(p)
}

// Link is the SMS link for a listing.
func (s *Service) Link(id uuid.UUID) string { return s.baseURL + "/c/" + s.Token(id) }

// Open checks a token and returns its listing ID.
func (s *Service) Open(token string) (uuid.UUID, error) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return uuid.Nil, ErrBadLink
	}
	p, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(p) != 20 || !hmac.Equal([]byte(sig), []byte(s.sign(p))) {
		return uuid.Nil, ErrBadLink
	}
	if s.now().Unix() > int64(binary.BigEndian.Uint32(p[16:])) {
		return uuid.Nil, ErrBadLink
	}
	return uuid.UUID(p[:16]), nil
}

// ── Freshness ────────────────────────────────────────────────────────────

// Fresh and Archived are the listing package's rules.
func Fresh(l *ent.Listing, now time.Time) bool    { return listings.Fresh(l, now) }
func Archived(l *ent.Listing, now time.Time) bool { return listings.Archived(l, now) }

func quiet(now time.Time) bool { h := now.Hour(); return h >= QuietFrom || h < QuietUntil }

// Sweep is the hourly job: expire listings unconfirmed for 14 days, and
// nudge those that are ageing. It returns (expired, nudged).
func (s *Service) Sweep(ctx context.Context) (int, int, error) {
	now := s.now()
	stale, err := s.db.Listing.Query().Where(listing.StatusEQ(listing.StatusActive),
		listing.Or(listing.LastConfirmedAtLT(now.Add(-ExpireAfter)), listing.LastConfirmedAtIsNil())).All(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("availability: sweep: %w", err)
	}
	expired := 0
	for _, l := range stale {
		if l.LastConfirmedAt == nil && l.PublishedAt != nil && now.Sub(*l.PublishedAt) < ExpireAfter {
			continue
		}
		n, err := s.db.Listing.Update().Where(listing.ID(l.ID), listing.StatusEQ(listing.StatusActive)).
			SetStatus(listing.StatusExpired).SetNudgedAt(now).Save(ctx)
		if err != nil || n == 0 {
			continue
		}
		expired++
		s.audit.Record(ctx, audit.Event{Action: "listing.expired", TargetType: "listing", TargetID: l.ID.String()})
		if !quiet(now) {
			s.textLister(ctx, l, "RentMap: %s is now hidden from search because it hasn't been confirmed in 2 weeks. Still available? %s")
		}
	}
	if quiet(now) {
		return expired, 0, nil
	}
	ageing, err := s.db.Listing.Query().Where(listing.StatusEQ(listing.StatusActive), listing.Or(
		listing.LastConfirmedAtLT(now.Add(-FreshFor)),
		listing.StaleReportedAtNotNil(),
	)).All(ctx)
	if err != nil {
		return expired, 0, fmt.Errorf("availability: ageing: %w", err)
	}
	nudged := 0
	for _, l := range ageing {
		if !s.dueNudge(l, now) {
			continue
		}
		if err := s.db.Listing.UpdateOne(l).SetNudgedAt(now).Exec(ctx); err != nil {
			continue
		}
		nudged++
		s.textLister(ctx, l, "RentMap: is %s still available? Tap to answer (no login): %s")
	}
	return expired, nudged, nil
}

// dueNudge decides whether an ageing listing gets a text now.
func (s *Service) dueNudge(l *ent.Listing, now time.Time) bool {
	if Fresh(l, now) {
		return false
	}
	reported := l.StaleReportedAt != nil && (l.LastConfirmedAt == nil || l.StaleReportedAt.After(*l.LastConfirmedAt))
	switch {
	case l.NudgedAt == nil:
		return true
	case reported && l.NudgedAt.Before(*l.StaleReportedAt) && now.Sub(*l.NudgedAt) >= StaleNudgeGap:
		return true
	case l.LastConfirmedAt != nil && l.NudgedAt.Before(*l.LastConfirmedAt):
		return true // confirmed since the last nudge: this is a new round
	}
	return now.Sub(*l.NudgedAt) >= RenudgeAfter
}

func (s *Service) textLister(ctx context.Context, l *ent.Listing, format string) {
	u, err := s.db.User.Get(ctx, l.ListerID)
	if err != nil || u.Phone == nil {
		return
	}
	what := "your listing"
	if h := []rune(l.Headline); len(h) > 0 {
		if len(h) > 36 {
			h = append(h[:35], '…')
		}
		what = "\"" + string(h) + "\""
	}
	body := fmt.Sprintf(format, what, s.Link(l.ID))
	if s.notify != nil {
		// Quiet hours are handled by the sweep itself; the link page is the "URL".
		s.notify.SendTo(ctx, u, notify.Note{Topic: "listings", Kind: "listing.check", Title: "Is " + what + " still available?",
			URL: "/listings", SMS: body, Urgent: true})
		return
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
	defer cancel()
	if err := s.sms.Send(sendCtx, sms.Message{To: *u.Phone, Body: body}); err != nil {
		slog.WarnContext(ctx, "availability: sms", "err", err)
	}
}

// ── The lister's answer ──────────────────────────────────────────────────

// Answer is the lister's reply to "still available?".
type Answer string

const (
	StillAvailable Answer = "available"
	IsRented       Answer = "rented"
	PauseIt        Answer = "pause"
)

// Listing loads a listing for its confirmation page.
func (s *Service) Listing(ctx context.Context, id uuid.UUID) (*ent.Listing, error) {
	l, err := s.db.Listing.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("availability: listing: %w", err)
	}
	return l, nil
}

// Confirm applies the lister's answer. a is the lister (from the link or a
// session); rentedVia is used when the answer is IsRented.
func (s *Service) Confirm(ctx context.Context, a Actor, id uuid.UUID, ans Answer, rentedVia string) (*ent.Listing, error) {
	l, err := s.Listing(ctx, id)
	if err != nil {
		return nil, err
	}
	if l.ListerID != a.UserID {
		return nil, ErrNotFound
	}
	now := s.now()
	from := listings.Status(l.Status)
	up := s.db.Listing.Update().Where(listing.ID(id), listing.StatusEQ(l.Status))
	var action string
	switch ans {
	case StillAvailable:
		if from != listings.Active {
			if _, err := listings.Next(from, listings.EvResume); err != nil {
				return nil, ValidationError{"form": "This listing can't be marked available from here. Open it in Your listings."}
			}
			up.SetStatus(listing.StatusActive)
		}
		up.SetLastConfirmedAt(now).ClearStaleReportedAt()
		action = "listing.confirmed"
	case PauseIt:
		if _, err := listings.Next(from, listings.EvPause); err != nil {
			return nil, ValidationError{"form": "Only a live listing can be paused."}
		}
		up.SetStatus(listing.StatusPaused)
		action = "listing.pause"
	case IsRented:
		if _, err := listings.Next(from, listings.EvMarkRented); err != nil {
			if from == listings.Rented { // already rented: just record the answer
				return s.setRentedVia(ctx, l, rentedVia)
			}
			return nil, ValidationError{"form": "This listing can't be marked rented from here."}
		}
		up.SetStatus(listing.StatusRented).SetRentedAt(now).SetRentedVia(via(rentedVia))
		action = "listing.mark_rented"
	default:
		return nil, ValidationError{"form": "Choose an answer."}
	}
	n, err := up.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("availability: confirm: %w", err)
	}
	if n == 0 {
		return nil, ValidationError{"form": "This listing just changed. Refresh and try again."}
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: action, TargetType: "listing", TargetID: id.String(),
		IP: a.IP, UserAgent: a.UserAgent, Meta: map[string]any{"rented_via": rentedVia}})
	if ans == IsRented || ans == PauseIt {
		reason := "rented"
		if ans == PauseIt {
			reason = "other"
		}
		if n, err := s.viewings.CloseForListing(ctx, id, reason); err != nil {
			slog.WarnContext(ctx, "availability: close viewings", "err", err)
		} else if n > 0 {
			slog.InfoContext(ctx, "availability: viewings closed", "listing", id, "count", n)
		}
	}
	return s.Listing(ctx, id)
}

func via(v string) listing.RentedVia {
	switch v {
	case "rentmap":
		return listing.RentedViaRentmap
	case "elsewhere":
		return listing.RentedViaElsewhere
	}
	return listing.RentedViaUnknown
}

func (s *Service) setRentedVia(ctx context.Context, l *ent.Listing, v string) (*ent.Listing, error) {
	return s.db.Listing.UpdateOne(l).SetRentedVia(via(v)).Save(ctx)
}

// ── Renters ──────────────────────────────────────────────────────────────

// ReportRented is a renter saying a live listing is already rented: the
// listing stops showing "confirmed available" at once and the lister is
// asked to confirm (by the next sweep, or now if it's daytime).
func (s *Service) ReportRented(ctx context.Context, a Actor, id uuid.UUID) error {
	l, err := s.Listing(ctx, id)
	if err != nil {
		return err
	}
	if l.Status != listing.StatusActive || l.ListerID == a.UserID {
		return ErrNotFound
	}
	dup, err := s.db.Report.Query().Where(report.ReporterID(a.UserID), report.TargetTypeEQ(report.TargetTypeListing),
		report.TargetID(id), report.ReasonEQ("rented"), report.CreatedAtGT(s.now().Add(-30*24*time.Hour))).Exist(ctx)
	if err != nil {
		return fmt.Errorf("availability: report dup: %w", err)
	}
	if dup {
		return nil
	}
	now := s.now()
	if err := s.db.Report.Create().SetReporterID(a.UserID).SetTargetType(report.TargetTypeListing).SetTargetID(id).
		SetSubjectID(l.ListerID).SetReason("rented").SetStatus(report.StatusActioned).Exec(ctx); err != nil {
		return fmt.Errorf("availability: report: %w", err)
	}
	if l, err = s.db.Listing.UpdateOne(l).SetStaleReportedAt(now).Save(ctx); err != nil {
		return fmt.Errorf("availability: stale: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: "listing.reported_rented", TargetType: "listing", TargetID: id.String(), IP: a.IP, UserAgent: a.UserAgent})
	if !quiet(now) && s.dueNudge(l, now) {
		if err := s.db.Listing.UpdateOne(l).SetNudgedAt(now).Exec(ctx); err == nil {
			s.textLister(ctx, l, "RentMap: a renter says %s may already be rented. Is it still available? %s")
		}
	}
	return nil
}
