// Package viewings books renters in to see a place (ProjectRequirement
// §6.8): the renter picks one of the lister's weekly slots (or suggests a
// time), the lister accepts, proposes another time or declines. While a
// viewing is confirmed — and only then — the renter sees the exact location
// and both sides see each other's phone number.
package viewings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/ent/viewing"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/notify"
	"rentmapgh/internal/platform/money"
	"rentmapgh/internal/platform/sms"
	"rentmapgh/internal/platform/weekly"
)

// Ghana keeps GMT all year (no daylight saving), so a fixed zone is exact
// and needs no tzdata in the image.
var Accra = time.FixedZone("GMT", 0)

const (
	SlotLength   = 30 * time.Minute
	MinNotice    = 3 * time.Hour       // earliest bookable slot from now
	Horizon      = 14 * 24 * time.Hour // how far ahead renters can book
	SuggestFrom  = 7 * 60              // suggested times: 7:00…
	SuggestUntil = 18 * 60             // …to 18:00
	MaxOpen      = 5                   // open requests per renter
	DailyLimit   = 10                  // requests per renter per day
	// UnlockAfter keeps the exact location visible for a day after the
	// start, for late arrivals and "where was it again?".
	UnlockAfter = 24 * time.Hour
)

// DeclineReasons are what a lister can say when declining.
var DeclineReasons = []struct{ Key, Label string }{
	{"rented", "It's already rented"},
	{"time", "That time doesn't work"},
	{"not_suitable", "Not suitable for this renter"},
	{"other", "Another reason"},
}

var (
	ErrNotFound  = errors.New("viewings: not found")
	ErrForbidden = errors.New("viewings: not yours")
)

// ValidationError maps field → message.
type ValidationError map[string]string

func (v ValidationError) Error() string { return fmt.Sprintf("viewings: %d invalid field(s)", len(v)) }

// Actor is the signed-in user.
type Actor struct {
	UserID        uuid.UUID
	IP, UserAgent string
}

type Service struct {
	db      *ent.Client
	audit   *audit.Log
	sms     sms.Sender      // used directly only when no notifier is set (tests)
	notify  *notify.Service // in-app + SMS per the user's preferences
	baseURL string
	now     func() time.Time
}

// SetNotifier routes messages through the notification centre.
func (s *Service) SetNotifier(n *notify.Service) { s.notify = n }

func NewService(db *ent.Client, log *audit.Log, sender sms.Sender, baseURL string) *Service {
	return &Service{db: db, audit: log, sms: sender, baseURL: strings.TrimRight(baseURL, "/"), now: func() time.Time { return time.Now().UTC() }}
}

// Now is the service clock (tests move it).
func (s *Service) Now() time.Time { return s.now() }

// open states hold a time for the renter and the lister.
var openStates = []viewing.Status{viewing.StatusRequested, viewing.StatusProposed, viewing.StatusConfirmed}

// ── The listing being viewed ─────────────────────────────────────────────

// Place is the listing with what a viewing needs.
type Place struct {
	L      *ent.Listing
	U      *ent.Unit
	P      *ent.Property
	T      *ent.ListingTerms
	Lister *ent.User
}

// Fee is the viewing fee, if any.
func (p *Place) Fee() *money.Pesewas {
	if p.T != nil && p.T.ViewingFee != nil && *p.T.ViewingFee > 0 {
		return p.T.ViewingFee
	}
	return nil
}

func (s *Service) place(ctx context.Context, listingID uuid.UUID) (*Place, error) {
	l, err := s.db.Listing.Query().Where(listing.ID(listingID)).
		WithTerms().WithUnit(func(q *ent.UnitQuery) { q.WithProperty() }).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("viewings: listing: %w", err)
	}
	if l.Edges.Unit == nil || l.Edges.Unit.Edges.Property == nil {
		return nil, ErrNotFound
	}
	lister, err := s.db.User.Get(ctx, l.ListerID)
	if err != nil {
		return nil, fmt.Errorf("viewings: lister: %w", err)
	}
	return &Place{L: l, U: l.Edges.Unit, P: l.Edges.Unit.Edges.Property, T: l.Edges.Terms, Lister: lister}, nil
}

// Bookable returns a live listing a renter may ask to view.
func (s *Service) Bookable(ctx context.Context, a Actor, listingID uuid.UUID) (*Place, error) {
	p, err := s.place(ctx, listingID)
	if err != nil {
		return nil, err
	}
	if p.L.Status != listing.StatusActive {
		return nil, ErrNotFound
	}
	if p.L.ListerID == a.UserID {
		return nil, ErrForbidden
	}
	return p, nil
}

// ── Slots ────────────────────────────────────────────────────────────────

// Slots are the lister's free viewing times over the next two weeks. An
// empty result with no hours set means "suggest a time".
func (s *Service) Slots(ctx context.Context, p *Place) ([]time.Time, error) {
	hours := weekly.Normalize(p.Lister.ViewingHours)
	if len(hours) == 0 {
		return nil, nil
	}
	now := s.now()
	taken, err := s.db.Viewing.Query().
		Where(viewing.ListerID(p.L.ListerID), viewing.StatusIn(openStates...), viewing.StartsAtGTE(now)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("viewings: taken slots: %w", err)
	}
	busy := func(t time.Time) bool {
		end := t.Add(SlotLength)
		for _, v := range taken {
			vEnd := v.StartsAt.Add(time.Duration(v.DurationMin) * time.Minute)
			if t.Before(vEnd) && v.StartsAt.Before(end) && v.Status == viewing.StatusConfirmed {
				return true
			}
		}
		return false
	}
	return weekly.Slots(hours, Accra, now, now.Add(Horizon), now.Add(MinNotice), SlotLength, busy), nil
}

// checkTime validates a requested or proposed time.
func (s *Service) checkTime(ctx context.Context, p *Place, t time.Time, fromSlots bool) error {
	now := s.now()
	switch {
	case t.Before(now.Add(MinNotice)):
		return ValidationError{"starts_at": "Pick a time at least 3 hours from now."}
	case t.After(now.Add(Horizon)):
		return ValidationError{"starts_at": "Pick a time within the next two weeks."}
	}
	if !fromSlots {
		m := t.In(Accra).Hour()*60 + t.In(Accra).Minute()
		if m < SuggestFrom || m+int(SlotLength.Minutes()) > SuggestUntil || m%30 != 0 {
			return ValidationError{"starts_at": "Suggest a time between 7:00 and 18:00, on the hour or half hour."}
		}
		return nil
	}
	slots, err := s.Slots(ctx, p)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(slots, func(x time.Time) bool { return x.Equal(t) }) {
		return ValidationError{"starts_at": "That time was just taken. Please pick another."}
	}
	return nil
}

// ── Renter: requesting ───────────────────────────────────────────────────

// Request is the renter's booking form.
type Request struct {
	StartsAt time.Time
	Note     string
	FeeAck   bool
}

// Ask books a viewing request.
func (s *Service) Ask(ctx context.Context, a Actor, listingID uuid.UUID, req Request) (*ent.Viewing, error) {
	p, err := s.Bookable(ctx, a, listingID)
	if err != nil {
		return nil, err
	}
	if v, err := s.OpenFor(ctx, a.UserID, listingID); err != nil {
		return nil, err
	} else if v != nil {
		return nil, ValidationError{"form": "You already have a viewing for this place. Open it from Viewings."}
	}
	note := strings.TrimSpace(req.Note)
	errs := ValidationError{}
	if utf8.RuneCountInString(note) > 300 {
		errs["note"] = "Keep the message under 300 characters."
	}
	fee := p.Fee()
	if fee != nil && !req.FeeAck {
		errs["fee_ack"] = "Please confirm you've seen the viewing fee."
	}
	if len(errs) > 0 {
		return nil, errs
	}
	hasHours := len(weekly.Normalize(p.Lister.ViewingHours)) > 0
	if err := s.checkTime(ctx, p, req.StartsAt, hasHours); err != nil {
		return nil, err
	}
	now := s.now()
	open, err := s.db.Viewing.Query().Where(viewing.RenterID(a.UserID), viewing.StatusIn(openStates...), viewing.StartsAtGT(now)).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("viewings: count open: %w", err)
	}
	today, err := s.db.Viewing.Query().Where(viewing.RenterID(a.UserID), viewing.CreatedAtGT(now.Add(-24*time.Hour))).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("viewings: count today: %w", err)
	}
	switch {
	case open >= MaxOpen:
		return nil, ValidationError{"form": fmt.Sprintf("You have %d viewings waiting. Cancel one or wait for replies first.", MaxOpen)}
	case today >= DailyLimit:
		return nil, ValidationError{"form": "That's a lot of requests for one day. Try again tomorrow."}
	}
	cr := s.db.Viewing.Create().SetListingID(listingID).SetRenterID(a.UserID).SetListerID(p.L.ListerID).
		SetStartsAt(req.StartsAt.UTC()).SetNote(note).SetFeeAcknowledged(req.FeeAck)
	if fee != nil {
		cr.SetViewingFee(*fee)
	}
	v, err := cr.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("viewings: create: %w", err)
	}
	s.record(ctx, a, "viewing.requested", v, nil)
	renter, _ := s.db.User.Get(ctx, a.UserID)
	s.tell(ctx, p.Lister, "viewing.requested", firstName(renter)+" wants to view "+headline(p)+" on "+When(v.StartsAt), s.path(v), false,
		fmt.Sprintf("RentMap: %s wants to view %s on %s. Reply: %s", firstName(renter), unitLabel(p), When(v.StartsAt), s.link(v)))
	return v, nil
}

// OpenFor is the renter's upcoming or pending viewing of a listing, if any.
func (s *Service) OpenFor(ctx context.Context, renterID, listingID uuid.UUID) (*ent.Viewing, error) {
	v, err := s.db.Viewing.Query().Where(viewing.RenterID(renterID), viewing.ListingID(listingID),
		viewing.StatusIn(openStates...), viewing.StartsAtGT(s.now().Add(-2*time.Hour))).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("viewings: open for: %w", err)
	}
	return v, nil
}

// ── Loading ──────────────────────────────────────────────────────────────

// Detail is a viewing with everything its page shows.
type Detail struct {
	V      *ent.Viewing
	Place  *Place
	Renter *ent.User
	Role   string // "renter" or "lister": who is looking
}

// Load returns a viewing for its renter or lister; anyone else gets
// ErrNotFound (so IDs can't be probed).
func (s *Service) Load(ctx context.Context, a Actor, id uuid.UUID) (*Detail, error) {
	v, err := s.db.Viewing.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("viewings: load: %w", err)
	}
	role := ""
	switch a.UserID {
	case v.RenterID:
		role = "renter"
	case v.ListerID:
		role = "lister"
	default:
		return nil, ErrNotFound
	}
	p, err := s.place(ctx, v.ListingID)
	if err != nil {
		return nil, err
	}
	renter, err := s.db.User.Get(ctx, v.RenterID)
	if err != nil {
		return nil, fmt.Errorf("viewings: renter: %w", err)
	}
	return &Detail{V: v, Place: p, Renter: renter, Role: role}, nil
}

// Unlocked reports whether the exact location and phone numbers are shared.
func (s *Service) Unlocked(v *ent.Viewing) bool {
	return v.Status == viewing.StatusConfirmed && s.now().Before(v.StartsAt.Add(UnlockAfter))
}

// SeeLocation records the first time the renter opens the exact location
// (ProjectRequirement §6.1 rule 5: "and it's logged").
func (s *Service) SeeLocation(ctx context.Context, a Actor, d *Detail) {
	if d.Role != "renter" || !s.Unlocked(d.V) || d.V.LocationSeenAt != nil {
		return
	}
	now := s.now()
	if err := s.db.Viewing.UpdateOne(d.V).SetLocationSeenAt(now).Exec(ctx); err != nil {
		slog.WarnContext(ctx, "viewings: location seen", "err", err)
		return
	}
	d.V.LocationSeenAt = &now
	s.record(ctx, a, "viewing.location_unlocked", d.V, map[string]any{"property": d.Place.P.ID.String()})
}

// Mine lists the actor's viewings, as renter and as lister, soonest first
// (past ones after upcoming ones).
func (s *Service) Mine(ctx context.Context, a Actor) (asRenter, asLister []*Detail, err error) {
	vs, err := s.db.Viewing.Query().Where(viewing.Or(viewing.RenterID(a.UserID), viewing.ListerID(a.UserID))).
		Order(ent.Desc(viewing.FieldStartsAt)).Limit(200).All(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("viewings: mine: %w", err)
	}
	places := map[uuid.UUID]*Place{}
	people := map[uuid.UUID]*ent.User{}
	for _, v := range vs {
		p, ok := places[v.ListingID]
		if !ok {
			if p, err = s.place(ctx, v.ListingID); err != nil {
				continue
			}
			places[v.ListingID] = p
		}
		r, ok := people[v.RenterID]
		if !ok {
			if r, err = s.db.User.Get(ctx, v.RenterID); err != nil {
				continue
			}
			people[v.RenterID] = r
		}
		d := &Detail{V: v, Place: p, Renter: r}
		if v.RenterID == a.UserID {
			d.Role = "renter"
			asRenter = append(asRenter, d)
		} else {
			d.Role = "lister"
			asLister = append(asLister, d)
		}
	}
	now := s.now()
	order := func(ds []*Detail) {
		slices.SortStableFunc(ds, func(x, y *Detail) int {
			xp, yp := x.V.StartsAt.Before(now), y.V.StartsAt.Before(now)
			if xp != yp {
				if xp {
					return 1
				}
				return -1
			}
			if xp {
				return y.V.StartsAt.Compare(x.V.StartsAt) // past: most recent first
			}
			return x.V.StartsAt.Compare(y.V.StartsAt) // upcoming: soonest first
		})
	}
	order(asRenter)
	order(asLister)
	return asRenter, asLister, nil
}

// Pending counts requests waiting for the actor's answer (nav badge).
func (s *Service) Pending(ctx context.Context, userID uuid.UUID) (int, error) {
	now := s.now()
	return s.db.Viewing.Query().Where(viewing.Or(
		viewing.And(viewing.ListerID(userID), viewing.StatusEQ(viewing.StatusRequested)),
		viewing.And(viewing.RenterID(userID), viewing.StatusEQ(viewing.StatusProposed)),
	), viewing.StartsAtGT(now)).Count(ctx)
}

// ── Answers ──────────────────────────────────────────────────────────────

// Action is a button on the viewing page.
type Action string

const (
	Accept         Action = "accept"          // lister: confirm the requested time
	AcceptProposal Action = "accept-proposal" // renter: take the lister's new time
	Propose        Action = "propose"         // lister: suggest another time
	Decline        Action = "decline"         // lister: say no
	Cancel         Action = "cancel"          // either: call it off
	Done           Action = "done"            // lister, after the time: it happened
	NoShow         Action = "no-show"         // lister, after the time: renter didn't come
)

// Answer carries an action's extra fields.
type Answer struct {
	StartsAt time.Time // propose
	Reason   string    // decline
}

// Act applies an action and tells the other side by SMS.
func (s *Service) Act(ctx context.Context, a Actor, id uuid.UUID, act Action, ans Answer) (*Detail, error) {
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return nil, err
	}
	v, now := d.V, s.now()
	upcoming := v.StartsAt.After(now)
	up := s.db.Viewing.Update().Where(viewing.ID(v.ID), viewing.StatusEQ(v.Status)) // only from the state we showed
	var tell *ent.User
	var msg, title string
	answered := d.Role == "lister" && v.RespondedAt == nil && (act == Accept || act == Propose || act == Decline)
	switch {
	case act == Accept && d.Role == "lister" && v.Status == viewing.StatusRequested && upcoming:
		if err := s.checkFree(ctx, v, v.StartsAt); err != nil {
			return nil, err
		}
		up.SetStatus(viewing.StatusConfirmed).SetConfirmedAt(now)
		tell, msg = d.Renter, fmt.Sprintf("RentMap: your viewing of %s on %s is confirmed. Address and directions: %s", unitLabel(d.Place), When(v.StartsAt), s.link(v))
		title = "Viewing confirmed: " + When(v.StartsAt) + " · address unlocked"
	case act == AcceptProposal && d.Role == "renter" && v.Status == viewing.StatusProposed && upcoming:
		if err := s.checkFree(ctx, v, v.StartsAt); err != nil {
			return nil, err
		}
		up.SetStatus(viewing.StatusConfirmed).SetConfirmedAt(now)
		tell, msg = d.Place.Lister, fmt.Sprintf("RentMap: %s accepted %s for %s. Details: %s", firstName(d.Renter), When(v.StartsAt), unitLabel(d.Place), s.link(v))
		title = firstName(d.Renter) + " accepted " + When(v.StartsAt)
	case act == Propose && d.Role == "lister" && (v.Status == viewing.StatusRequested || v.Status == viewing.StatusProposed):
		if err := s.checkTime(ctx, d.Place, ans.StartsAt, false); err != nil {
			return nil, err
		}
		up.SetStatus(viewing.StatusProposed).SetStartsAt(ans.StartsAt.UTC())
		tell, msg = d.Renter, fmt.Sprintf("RentMap: the lister of %s suggests %s instead. Accept or decline: %s", unitLabel(d.Place), When(ans.StartsAt), s.link(v))
		title = "New time suggested: " + When(ans.StartsAt)
	case act == Decline && d.Role == "lister" && (v.Status == viewing.StatusRequested || v.Status == viewing.StatusProposed):
		reason := ans.Reason
		if !slices.ContainsFunc(DeclineReasons, func(r struct{ Key, Label string }) bool { return r.Key == reason }) {
			reason = "other"
		}
		up.SetStatus(viewing.StatusDeclined).SetDeclineReason(reason).SetClosedBy(a.UserID)
		title = "Viewing declined: " + ReasonLabel(reason)
		tell, msg = d.Renter, fmt.Sprintf("RentMap: the viewing of %s on %s can't go ahead (%s). Find similar places: %s/search",
			unitLabel(d.Place), When(v.StartsAt), strings.ToLower(ReasonLabel(reason)), s.baseURL)
	case act == Cancel && slices.Contains(openStates, v.Status) && upcoming:
		up.SetStatus(viewing.StatusCancelled).SetClosedBy(a.UserID)
		tell = d.Place.Lister
		if d.Role == "lister" {
			tell = d.Renter
		}
		title = "Viewing cancelled: " + When(v.StartsAt)
		if v.Status != viewing.StatusRequested || d.Role == "lister" { // a bare request withdrawn needs no text
			msg = fmt.Sprintf("RentMap: the viewing of %s on %s was cancelled. %s", unitLabel(d.Place), When(v.StartsAt), s.link(v))
		}
	case (act == Done || act == NoShow) && d.Role == "lister" && v.Status == viewing.StatusConfirmed && !upcoming:
		st := viewing.StatusCompleted
		if act == NoShow {
			st = viewing.StatusNoShow
		}
		up.SetStatus(st)
	default:
		return nil, ValidationError{"form": "That can't be done now. The page may be out of date — refresh it."}
	}
	if answered {
		up.SetRespondedAt(now)
	}
	n, err := up.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("viewings: act: %w", err)
	}
	if n == 0 {
		return nil, ValidationError{"form": "Someone just changed this viewing. Refresh to see it."}
	}
	d, err = s.Load(ctx, a, id)
	if err != nil {
		return nil, err
	}
	s.record(ctx, a, "viewing."+strings.ReplaceAll(string(act), "-", "_"), d.V, nil)
	if tell != nil && msg != "" {
		s.tell(ctx, tell, "viewing."+strings.ReplaceAll(string(act), "-", "_"), title, s.path(v), false, msg)
	}
	return d, nil
}

// checkFree refuses to confirm over another confirmed viewing of the lister's.
func (s *Service) checkFree(ctx context.Context, v *ent.Viewing, t time.Time) error {
	end := t.Add(time.Duration(v.DurationMin) * time.Minute)
	clash, err := s.db.Viewing.Query().Where(viewing.ListerID(v.ListerID), viewing.StatusEQ(viewing.StatusConfirmed),
		viewing.IDNEQ(v.ID), viewing.StartsAtLT(end), viewing.StartsAtGT(t.Add(-2*time.Hour))).All(ctx)
	if err != nil {
		return fmt.Errorf("viewings: clash: %w", err)
	}
	for _, c := range clash {
		if c.StartsAt.Add(time.Duration(c.DurationMin) * time.Minute).After(t) {
			return ValidationError{"form": "Another viewing is already confirmed at that time. Propose a different time instead."}
		}
	}
	return nil
}

// ── Hours ────────────────────────────────────────────────────────────────

// SetHours saves the lister's weekly viewing hours.
func (s *Service) SetHours(ctx context.Context, a Actor, ws []weekly.Window) ([]weekly.Window, error) {
	ws = weekly.Normalize(ws)
	if err := s.db.User.UpdateOneID(a.UserID).SetViewingHours(ws).Exec(ctx); err != nil {
		return nil, fmt.Errorf("viewings: hours: %w", err)
	}
	return ws, nil
}

// Hours returns the lister's weekly viewing hours.
func (s *Service) Hours(ctx context.Context, userID uuid.UUID) ([]weekly.Window, error) {
	u, err := s.db.User.Query().Where(user.ID(userID)).Select(user.FieldViewingHours).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("viewings: hours: %w", err)
	}
	return weekly.Normalize(u.ViewingHours), nil
}

// ── Helpers ──────────────────────────────────────────────────────────────

// When renders a time for SMS and pages: "Sat 4 Oct, 10:00".
func When(t time.Time) string { return t.In(Accra).Format("Mon 2 Jan, 15:04") }

// ReasonLabel is the words for a decline reason.
func ReasonLabel(key string) string {
	for _, r := range DeclineReasons {
		if r.Key == key {
			return r.Label
		}
	}
	return ""
}

func firstName(u *ent.User) string {
	if u == nil || u.Name == "" {
		return "A renter"
	}
	name, _, _ := strings.Cut(u.Name, " ")
	return name
}

func unitLabel(p *Place) string {
	if p.L.Headline != "" {
		h := []rune(p.L.Headline)
		if len(h) > 40 {
			return "\"" + string(h[:39]) + "…\""
		}
		return "\"" + p.L.Headline + "\""
	}
	return "your listing"
}

func (s *Service) link(v *ent.Viewing) string { return s.baseURL + "/viewings/" + v.ID.String() }

func (s *Service) path(v *ent.Viewing) string { return "/viewings/" + v.ID.String() }

// tell notifies a user: through the notification centre when set (in-app +
// SMS per preferences), else by SMS directly.
func (s *Service) tell(ctx context.Context, to *ent.User, kind, title, url string, urgent bool, smsBody string) {
	if to == nil {
		return
	}
	if s.notify != nil {
		s.notify.SendTo(ctx, to, notify.Note{Topic: "viewings", Kind: kind, Title: title, URL: url, SMS: smsBody, Urgent: urgent})
		return
	}
	s.text(ctx, to, smsBody)
}

func (s *Service) text(ctx context.Context, to *ent.User, body string) {
	if to == nil || to.Phone == nil {
		return
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
	defer cancel()
	if err := s.sms.Send(sendCtx, sms.Message{To: *to.Phone, Body: body}); err != nil {
		slog.WarnContext(ctx, "viewings: sms", "err", err) // the page shows the state anyway
	}
}

func (s *Service) record(ctx context.Context, a Actor, action string, v *ent.Viewing, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["listing"] = v.ListingID.String()
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: action, TargetType: "viewing", TargetID: v.ID.String(),
		IP: a.IP, UserAgent: a.UserAgent, Meta: meta})
}

// CloseForListing declines every open upcoming viewing of a listing (it was
// rented or taken down) and texts each renter. It returns how many closed.
func (s *Service) CloseForListing(ctx context.Context, listingID uuid.UUID, reason string) (int, error) {
	now := s.now()
	vs, err := s.db.Viewing.Query().Where(viewing.ListingID(listingID), viewing.StatusIn(openStates...), viewing.StartsAtGT(now)).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("viewings: close for listing: %w", err)
	}
	if len(vs) == 0 {
		return 0, nil
	}
	p, err := s.place(ctx, listingID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, v := range vs {
		k, err := s.db.Viewing.Update().Where(viewing.ID(v.ID), viewing.StatusEQ(v.Status)).
			SetStatus(viewing.StatusDeclined).SetDeclineReason(reason).SetClosedBy(v.ListerID).Save(ctx)
		if err != nil || k == 0 {
			continue
		}
		n++
		if renter, err := s.db.User.Get(ctx, v.RenterID); err == nil {
			s.tell(ctx, renter, "viewing.declined", "Viewing off: "+ReasonLabel(reason), s.path(v), false,
				fmt.Sprintf("RentMap: the viewing of %s on %s is off — %s. Find similar places: %s/search",
					unitLabel(p), When(v.StartsAt), strings.ToLower(ReasonLabel(reason)), s.baseURL))
		}
		s.audit.Record(ctx, audit.Event{Action: "viewing.auto_declined", TargetType: "viewing", TargetID: v.ID.String(),
			Meta: map[string]any{"listing": listingID.String(), "reason": reason}})
	}
	return n, nil
}
