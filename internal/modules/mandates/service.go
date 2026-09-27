// Package mandates lets an agent ask a landlord for permission to list a
// property. The landlord answers through a link sent to their phone by SMS
// (no account needed): approve, decline, "I don't know this person", or
// later withdraw. Agent listings without an approved mandate stay allowed
// but are labelled "owner authority not confirmed" (ProjectRequirement
// Phase 2).
package mandates

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/agentmandate"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/property"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/platform/sms"
)

// Audit actions.
const (
	ActRequested = "mandate.requested"
	ActResent    = "mandate.resent"
	ActCancelled = "mandate.cancelled"
	ActApproved  = "mandate.approved"
	ActDeclined  = "mandate.declined"
	ActReported  = "mandate.reported"
	ActRevoked   = "mandate.revoked"
)

// Limits.
const (
	DailySMS       = 10               // mandate texts per agent per 24 h
	MaxSends       = 3                // per request, including the first
	ResendCooldown = 10 * time.Minute // between texts for one request
	PendingTTL     = 14 * 24 * time.Hour
	DeclineCooloff = 7 * 24 * time.Hour // before asking the same property again
)

// Months the agent may ask for.
var MonthOptions = []int{6, 12}

var (
	ErrNotFound = errors.New("mandates: not found")
	ErrNoAgent  = errors.New("mandates: only agent listings need a mandate")
)

// ValidationError maps form field → message.
type ValidationError map[string]string

func (v ValidationError) Error() string { return fmt.Sprintf("mandates: %d invalid field(s)", len(v)) }

// Actor is the signed-in agent.
type Actor struct {
	UserID        uuid.UUID
	IP, UserAgent string
}

type Service struct {
	db      *ent.Client
	audit   *audit.Log
	sms     sms.Sender
	secret  []byte
	baseURL string
	now     func() time.Time
}

// NewService: secret keys the link-token HMAC (AUTH_SECRET is fine; the
// input is domain-separated), baseURL builds the link.
func NewService(db *ent.Client, log *audit.Log, sender sms.Sender, secret, baseURL string) *Service {
	return &Service{db: db, audit: log, sms: sender, secret: []byte(secret), baseURL: strings.TrimRight(baseURL, "/"),
		now: func() time.Time { return time.Now().UTC() }}
}

// ── State ─────────────────────────────────────────────────────────────────

// State is what a mandate means right now. Approved mandates past
// valid_until and pending ones past PendingTTL have lapsed.
type State string

const (
	None      State = ""
	Pending   State = "pending"
	Approved  State = "approved"
	Declined  State = "declined"
	Revoked   State = "revoked"
	Cancelled State = "cancelled"
	Expired   State = "expired"
)

// StateOf interprets a stored mandate at time now.
func StateOf(m *ent.AgentMandate, now time.Time) State {
	if m == nil {
		return None
	}
	switch m.Status {
	case agentmandate.StatusApproved:
		if m.ValidUntil != nil && !now.Before(*m.ValidUntil) {
			return Expired
		}
		return Approved
	case agentmandate.StatusPending:
		if now.Sub(m.SentAt) >= PendingTTL {
			return Expired
		}
		return Pending
	}
	return State(m.Status)
}

// Confirmed reports whether the owner's authority is currently confirmed.
func (st State) Confirmed() bool { return st == Approved }

// Label is the public wording for an agent listing.
func Label(st State) string {
	if st.Confirmed() {
		return "Owner authority confirmed"
	}
	return "Agent listing — owner authority not confirmed"
}

// Latest is the agent's most recent mandate for a property, or nil.
func (s *Service) Latest(ctx context.Context, agentID, propertyID uuid.UUID) (*ent.AgentMandate, error) {
	m, err := s.db.AgentMandate.Query().
		Where(agentmandate.AgentID(agentID), agentmandate.PropertyID(propertyID)).
		Order(ent.Desc(agentmandate.FieldCreatedAt)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mandates: latest: %w", err)
	}
	return m, nil
}

// States returns the current state for many (agent, property) pairs at
// once, keyed by property ID, for list pages.
func (s *Service) States(ctx context.Context, agentID uuid.UUID, propertyIDs []uuid.UUID) (map[uuid.UUID]State, error) {
	out := map[uuid.UUID]State{}
	if len(propertyIDs) == 0 {
		return out, nil
	}
	ms, err := s.db.AgentMandate.Query().
		Where(agentmandate.AgentID(agentID), agentmandate.PropertyIDIn(propertyIDs...)).
		Order(ent.Asc(agentmandate.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("mandates: states: %w", err)
	}
	now := s.now()
	for _, m := range ms {
		out[m.PropertyID] = StateOf(m, now) // later rows win
	}
	return out, nil
}

// Now is the service clock (tests move it).
func (s *Service) Now() time.Time { return s.now() }

// ── Agent side ────────────────────────────────────────────────────────────

// Request is the agent's form.
type Request struct {
	LandlordName  string
	LandlordPhone string
	Months        int
}

// agentListing loads the agent's own listing with its property.
func (s *Service) agentListing(ctx context.Context, a Actor, listingID uuid.UUID) (*ent.Listing, *ent.Property, error) {
	l, err := s.db.Listing.Query().Where(listing.ID(listingID), listing.ListerID(a.UserID)).
		WithUnit(func(q *ent.UnitQuery) { q.WithProperty() }).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("mandates: listing: %w", err)
	}
	if l.ListerKind != listing.ListerKindAgent {
		return nil, nil, ErrNoAgent
	}
	if l.Edges.Unit == nil || l.Edges.Unit.Edges.Property == nil {
		return nil, nil, ErrNotFound
	}
	return l, l.Edges.Unit.Edges.Property, nil
}

// Ask sends a mandate request for the property of the agent's listing.
func (s *Service) Ask(ctx context.Context, a Actor, listingID uuid.UUID, req Request) (*ent.AgentMandate, error) {
	_, p, err := s.agentListing(ctx, a, listingID)
	if err != nil {
		return nil, err
	}
	errs := ValidationError{}
	name := strings.Join(strings.Fields(req.LandlordName), " ")
	if utf8.RuneCountInString(name) > 80 {
		errs["landlord_name"] = "Keep the name under 80 characters."
	}
	e164, perr := phone.NormalizeGhana(req.LandlordPhone)
	if perr != nil {
		errs["landlord_phone"] = strings.Replace(perr.Error(), "your phone number", "the owner's phone number", 1)
	}
	months := req.Months
	if months != 6 && months != 12 {
		months = 12
	}
	agent, err := s.db.User.Get(ctx, a.UserID)
	if err != nil {
		return nil, fmt.Errorf("mandates: agent: %w", err)
	}
	if perr == nil && agent.Phone != nil && *agent.Phone == e164 {
		errs["landlord_phone"] = "That's your own number. Enter the owner's phone number."
	}
	if len(errs) > 0 {
		return nil, errs
	}

	now := s.now()
	if last, err := s.Latest(ctx, a.UserID, p.ID); err != nil {
		return nil, err
	} else if last != nil {
		switch st := StateOf(last, now); {
		case st == Pending:
			return nil, ValidationError{"form": "A request is already waiting for the owner. Resend it or cancel it first."}
		case st == Approved:
			return nil, ValidationError{"form": "The owner has already approved this property."}
		case (st == Declined || st == Revoked) && last.DecidedAt != nil && now.Sub(*last.DecidedAt) < DeclineCooloff:
			return nil, ValidationError{"form": "The owner said no recently. Speak with them before asking again."}
		}
	}
	if err := s.checkDaily(ctx, a.UserID); err != nil {
		return nil, err
	}

	token, hash := s.newToken()
	m, err := s.db.AgentMandate.Create().SetAgentID(a.UserID).SetPropertyID(p.ID).SetLandlordPhone(e164).
		SetLandlordName(name).SetMonths(months).SetTokenHash(hash).SetSentAt(now).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mandates: create: %w", err)
	}
	if err := s.send(ctx, e164, s.requestSMS(agent, p, token)); err != nil {
		_ = s.db.AgentMandate.DeleteOne(m).Exec(context.WithoutCancel(ctx))
		return nil, ValidationError{"form": "We couldn't send the text message. Check the number and try again."}
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: ActRequested, TargetType: "agent_mandate", TargetID: m.ID.String(),
		IP: a.IP, UserAgent: a.UserAgent, Meta: map[string]any{"property": p.ID.String(), "landlord": phone.Mask(e164), "months": months}})
	return m, nil
}

// checkDaily caps mandate texts per agent (SMS costs money and annoys
// people who get them unasked).
func (s *Service) checkDaily(ctx context.Context, agentID uuid.UUID) error {
	ms, err := s.db.AgentMandate.Query().
		Where(agentmandate.AgentID(agentID), agentmandate.SentAtGT(s.now().Add(-24*time.Hour))).All(ctx)
	if err != nil {
		return fmt.Errorf("mandates: daily: %w", err)
	}
	n := 0
	for _, m := range ms {
		n += m.Sends
	}
	if n >= DailySMS {
		return ValidationError{"form": fmt.Sprintf("You've sent %d owner requests today. Try again tomorrow.", DailySMS)}
	}
	return nil
}

// Resend texts a pending request again with a fresh link (the old link
// stops working).
func (s *Service) Resend(ctx context.Context, a Actor, listingID uuid.UUID) error {
	m, p, err := s.pendingFor(ctx, a, listingID)
	if err != nil {
		return err
	}
	switch {
	case m.Sends >= MaxSends:
		return ValidationError{"form": "You've sent this request 3 times. Call the owner, or cancel and try later."}
	case s.now().Sub(m.SentAt) < ResendCooldown:
		return ValidationError{"form": "Wait a few minutes before sending it again."}
	}
	if err := s.checkDaily(ctx, a.UserID); err != nil {
		return err
	}
	agent, err := s.db.User.Get(ctx, a.UserID)
	if err != nil {
		return fmt.Errorf("mandates: agent: %w", err)
	}
	token, hash := s.newToken()
	if err := s.db.AgentMandate.UpdateOne(m).SetTokenHash(hash).SetSentAt(s.now()).AddSends(1).Exec(ctx); err != nil {
		return fmt.Errorf("mandates: resend: %w", err)
	}
	if err := s.send(ctx, m.LandlordPhone, s.requestSMS(agent, p, token)); err != nil {
		return ValidationError{"form": "We couldn't send the text message. Try again in a moment."}
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: ActResent, TargetType: "agent_mandate", TargetID: m.ID.String(), IP: a.IP, UserAgent: a.UserAgent})
	return nil
}

// Cancel withdraws a pending request; its link stops working.
func (s *Service) Cancel(ctx context.Context, a Actor, listingID uuid.UUID) error {
	m, _, err := s.pendingFor(ctx, a, listingID)
	if err != nil {
		return err
	}
	if err := s.db.AgentMandate.UpdateOne(m).SetStatus(agentmandate.StatusCancelled).SetDecidedAt(s.now()).Exec(ctx); err != nil {
		return fmt.Errorf("mandates: cancel: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: ActCancelled, TargetType: "agent_mandate", TargetID: m.ID.String(), IP: a.IP, UserAgent: a.UserAgent})
	return nil
}

func (s *Service) pendingFor(ctx context.Context, a Actor, listingID uuid.UUID) (*ent.AgentMandate, *ent.Property, error) {
	_, p, err := s.agentListing(ctx, a, listingID)
	if err != nil {
		return nil, nil, err
	}
	m, err := s.Latest(ctx, a.UserID, p.ID)
	if err != nil {
		return nil, nil, err
	}
	if StateOf(m, s.now()) != Pending {
		return nil, nil, ValidationError{"form": "There's no request waiting for the owner."}
	}
	return m, p, nil
}

func (s *Service) requestSMS(agent *ent.User, p *ent.Property, token string) string {
	who := agent.Name
	if who == "" {
		who = "An agent"
	}
	if agent.Phone != nil {
		who += " (" + phone.Pretty(*agent.Phone) + ")"
	}
	what := "your property"
	if name := []rune(p.Name); len(name) > 30 {
		what = "\"" + string(name[:29]) + "…\""
	} else if len(name) > 0 {
		what = "\"" + p.Name + "\""
	}
	// Aim for one 160-character SMS; the link is ~70 of them.
	return "RentMap: " + who + " asks to list " + what + " for rent. Approve or decline: " + s.LinkURL(token)
}

// LinkURL is the landlord's link for a token.
func (s *Service) LinkURL(token string) string { return s.baseURL + "/m/" + token }

func (s *Service) send(ctx context.Context, to, body string) error {
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
	defer cancel()
	if err := s.sms.Send(sendCtx, sms.Message{To: to, Body: body}); err != nil {
		slog.WarnContext(ctx, "mandates: sms", "to", phone.Mask(to), "err", err)
		return err
	}
	return nil
}

// ── Tokens ────────────────────────────────────────────────────────────────

// newToken returns a 256-bit URL-safe token and its HMAC.
func (s *Service) newToken() (string, []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	t := base64.RawURLEncoding.EncodeToString(b)
	return t, s.hash(t)
}

func (s *Service) hash(token string) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte("agent-mandate:"))
	m.Write([]byte(token))
	return m.Sum(nil)
}

// ── Landlord side ─────────────────────────────────────────────────────────

// Offer is what the landlord sees behind the link.
type Offer struct {
	M     *ent.AgentMandate
	State State
	Agent *ent.User // with AgentProfile
	P     *ent.Property
}

// ByToken resolves a landlord link. Unknown or malformed tokens are
// ErrNotFound.
func (s *Service) ByToken(ctx context.Context, token string) (*Offer, error) {
	if len(token) != 43 {
		return nil, ErrNotFound
	}
	m, err := s.db.AgentMandate.Query().Where(agentmandate.TokenHash(s.hash(token))).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mandates: by token: %w", err)
	}
	agent, err := s.db.User.Query().Where(user.ID(m.AgentID)).WithAgentProfile().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("mandates: agent: %w", err)
	}
	p, err := s.db.Property.Query().Where(property.ID(m.PropertyID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("mandates: property: %w", err)
	}
	return &Offer{M: m, State: StateOf(m, s.now()), Agent: agent, P: p}, nil
}

// Decision is the landlord's answer.
type Decision string

const (
	Approve  Decision = "approve"
	Decline  Decision = "decline"
	Report   Decision = "report" // decline: "I don't know this person"
	Withdraw Decision = "withdraw"
)

// Visitor is whoever holds the link (no account).
type Visitor struct{ IP, UserAgent string }

// Decide applies the landlord's answer. Pending requests can be approved,
// declined or reported; approved ones can be withdrawn.
func (s *Service) Decide(ctx context.Context, token string, d Decision, v Visitor) (*Offer, error) {
	o, err := s.ByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	now := s.now()
	// Only from the state we showed: a double tap can't apply two answers.
	up := s.db.AgentMandate.Update().
		Where(agentmandate.ID(o.M.ID), agentmandate.StatusEQ(o.M.Status), agentmandate.TokenHash(o.M.TokenHash)).
		SetDecidedAt(now)
	var action string
	switch {
	case o.State == Pending && d == Approve:
		up.SetStatus(agentmandate.StatusApproved).SetValidUntil(now.AddDate(0, o.M.Months, 0))
		action = ActApproved
	case o.State == Pending && (d == Decline || d == Report):
		up.SetStatus(agentmandate.StatusDeclined).SetReported(d == Report)
		action = ActDeclined
		if d == Report {
			action = ActReported
		}
	case o.State == Approved && d == Withdraw:
		up.SetStatus(agentmandate.StatusRevoked)
		action = ActRevoked
	default:
		return nil, ValidationError{"form": "This request has already been answered or has expired."}
	}
	// Link the landlord's account (and the property's owner) when they have one.
	landlord, err := s.db.User.Query().Where(user.Phone(o.M.LandlordPhone), user.StatusEQ(user.StatusActive)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("mandates: landlord: %w", err)
	}
	if landlord != nil {
		up.SetGrantedBy(landlord.ID)
	}
	n, err := up.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mandates: decide: %w", err)
	}
	if n == 0 {
		return nil, ValidationError{"form": "This request has already been answered or has expired."}
	}
	if o.M, err = s.db.AgentMandate.Get(ctx, o.M.ID); err != nil {
		return nil, fmt.Errorf("mandates: reload: %w", err)
	}
	if landlord != nil && action == ActApproved && o.P.OwnerID == nil {
		if err := s.db.Property.UpdateOneID(o.P.ID).SetOwnerID(landlord.ID).Exec(ctx); err != nil {
			slog.WarnContext(ctx, "mandates: set owner", "err", err)
		}
	}
	var actor *uuid.UUID
	if landlord != nil {
		actor = &landlord.ID
	}
	s.audit.Record(ctx, audit.Event{Actor: actor, Action: action, TargetType: "agent_mandate", TargetID: o.M.ID.String(),
		IP: v.IP, UserAgent: v.UserAgent, Meta: map[string]any{"agent": o.M.AgentID.String(), "property": o.P.ID.String()}})
	o.State = StateOf(o.M, now)

	if o.Agent.Phone != nil {
		if msg := s.agentSMS(o, action); msg != "" {
			_ = s.send(ctx, *o.Agent.Phone, msg) // the listing page shows the answer anyway
		}
	}
	return o, nil
}

func (s *Service) agentSMS(o *Offer, action string) string {
	who := o.M.LandlordName
	if who == "" {
		who = "The owner"
	}
	what := "the property"
	if o.P.Name != "" {
		what = o.P.Name
	}
	switch action {
	case ActApproved:
		return "RentMap: " + who + " approved you to list " + what + ". Your listing now shows \"Owner authority confirmed\"."
	case ActDeclined, ActReported:
		return "RentMap: " + who + " declined your request to list " + what + "."
	case ActRevoked:
		return "RentMap: " + who + " withdrew your approval to list " + what + "."
	}
	return ""
}
