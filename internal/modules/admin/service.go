// Package admin is the back office beyond the review queues: people and
// roles, suspensions, the audit log, platform metrics, flagged messages
// and duplicate listings.
package admin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/auditevent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/message"
	"rentmapgh/internal/ent/predicate"
	"rentmapgh/internal/ent/report"
	"rentmapgh/internal/ent/roleassignment"
	"rentmapgh/internal/ent/session"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/ent/viewing"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/listings"
	"rentmapgh/internal/modules/notify"
	"rentmapgh/internal/platform/phone"
)

var (
	ErrNotFound  = errors.New("admin: not found")
	ErrForbidden = errors.New("admin: not allowed")
)

// Problem is a refusal to show the moderator ("You can't suspend yourself").
type Problem string

func (p Problem) Error() string { return string(p) }

// Actor is the staff member doing something.
type Actor struct {
	UserID        uuid.UUID
	Roles         []string
	IP, UserAgent string
}

func (a Actor) IsAdmin() bool { return slices.Contains(a.Roles, "admin") }

func (a Actor) event(action, targetType, targetID string, meta map[string]any) audit.Event {
	return audit.Event{Actor: &a.UserID, Action: action, TargetType: targetType, TargetID: targetID, IP: a.IP, UserAgent: a.UserAgent, Meta: meta}
}

type Service struct {
	db       *ent.Client
	audit    *audit.Log
	listings *listings.Service
	notify   *notify.Service
	now      func() time.Time
}

func NewService(db *ent.Client, log *audit.Log, l *listings.Service, n *notify.Service) *Service {
	return &Service{db: db, audit: log, listings: l, notify: n, now: func() time.Time { return time.Now().UTC() }}
}

// Roles a staff member can hand out.
var Roles = []string{"renter", "landlord", "agent", "field_verifier", "moderator", "admin"}

func isStaff(roles []string) bool {
	return slices.Contains(roles, "moderator") || slices.Contains(roles, "admin")
}

func roleNames(u *ent.User) []string {
	out := make([]string, 0, len(u.Edges.Roles))
	for _, r := range u.Edges.Roles {
		out = append(out, string(r.Role))
	}
	slices.Sort(out)
	return out
}

// ── Users ────────────────────────────────────────────────────────────────

const PageSize = 50

type UserFilter struct {
	Q, Role, Status string
	Page            int // from 0
}

// Users searches people by phone or name, newest first.
func (s *Service) Users(ctx context.Context, f UserFilter) ([]*ent.User, int, error) {
	q := s.db.User.Query()
	switch f.Status {
	case "active", "suspended", "deleted":
		q.Where(user.StatusEQ(user.Status(f.Status)))
	default:
		q.Where(user.StatusNEQ(user.StatusDeleted))
	}
	if slices.Contains(Roles, f.Role) {
		q.Where(user.HasRolesWith(roleassignment.RoleEQ(roleassignment.Role(f.Role))))
	}
	if t := strings.TrimSpace(f.Q); t != "" {
		if e164, err := phone.NormalizeGhana(t); err == nil {
			q.Where(user.Phone(e164))
		} else {
			q.Where(user.NameContainsFold(t))
		}
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("admin: users: %w", err)
	}
	us, err := q.WithRoles().Order(ent.Desc(user.FieldCreatedAt)).Offset(max(f.Page, 0) * PageSize).Limit(PageSize).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("admin: users: %w", err)
	}
	return us, total, nil
}

// UserDetail is everything support needs on one page.
type UserDetail struct {
	U            *ent.User
	Roles        []string
	Listings     []*ent.Listing
	Reports      []*ent.Report // about this user
	Events       []*ent.AuditEvent
	Sessions     int
	Viewings     int // as a renter
	NoShows      int
	ReportsFiled int
}

func (s *Service) User(ctx context.Context, id uuid.UUID) (*UserDetail, error) {
	u, err := s.db.User.Query().Where(user.ID(id)).WithRoles().Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("admin: user: %w", err)
	}
	d := &UserDetail{U: u, Roles: roleNames(u)}
	d.Listings, _ = s.db.Listing.Query().Where(listing.ListerID(id)).Order(ent.Desc(listing.FieldCreatedAt)).Limit(20).All(ctx)
	d.Reports, _ = s.db.Report.Query().Where(report.SubjectID(id)).Order(ent.Desc(report.FieldCreatedAt)).Limit(20).All(ctx)
	d.ReportsFiled, _ = s.db.Report.Query().Where(report.ReporterID(id)).Count(ctx)
	d.Events, _ = s.db.AuditEvent.Query().Where(auditevent.Or(auditevent.ActorID(id),
		auditevent.And(auditevent.TargetType("user"), auditevent.TargetID(id.String())))).
		Order(ent.Desc(auditevent.FieldCreatedAt)).Limit(30).All(ctx)
	d.Sessions, _ = s.db.Session.Query().Where(session.UserID(id), session.RevokedAtIsNil(), session.ExpiresAtGT(s.now()),
		session.ImpersonatorIDIsNil()).Count(ctx)
	d.Viewings, _ = s.db.Viewing.Query().Where(viewing.RenterID(id)).Count(ctx)
	d.NoShows, _ = s.db.Viewing.Query().Where(viewing.RenterID(id), viewing.StatusEQ(viewing.StatusNoShow)).Count(ctx)
	return d, nil
}

// target loads someone a staff member wants to act on and applies the
// ground rules: never yourself, and staff accounts only by an admin.
func (s *Service) target(ctx context.Context, a Actor, id uuid.UUID) (*ent.User, error) {
	u, err := s.db.User.Query().Where(user.ID(id)).WithRoles().Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if u.ID == a.UserID {
		return nil, Problem("You can't do that to your own account.")
	}
	return u, nil
}

// Suspend blocks sign-in, ends every session and pauses the user's live
// listings (they stay paused after reactivation until the lister resumes).
func (s *Service) Suspend(ctx context.Context, a Actor, id uuid.UUID, note string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		return Problem("Write down why — the next moderator will need it.")
	}
	if utf8.RuneCountInString(note) > 500 {
		return Problem("Keep the note under 500 characters.")
	}
	u, err := s.target(ctx, a, id)
	if err != nil {
		return err
	}
	if isStaff(roleNames(u)) {
		return Problem("Remove their staff role before suspending a staff account.")
	}
	now := s.now()
	n, err := s.db.User.Update().Where(user.ID(id), user.StatusEQ(user.StatusActive)).
		SetStatus(user.StatusSuspended).SetSuspendedAt(now).SetSuspensionNote(note).Save(ctx)
	if err != nil {
		return fmt.Errorf("admin: suspend: %w", err)
	}
	if n == 0 {
		return Problem("This account isn't active.")
	}
	sessions, _ := s.db.Session.Update().Where(session.UserID(id), session.RevokedAtIsNil()).SetRevokedAt(now).Save(ctx)
	paused, _ := s.db.Listing.Update().Where(listing.ListerID(id), listing.StatusEQ(listing.StatusActive)).
		SetStatus(listing.StatusPaused).Save(ctx)
	s.audit.Record(ctx, a.event("admin.user_suspended", "user", id.String(),
		map[string]any{"note": note, "sessions_ended": sessions, "listings_paused": paused}))
	return nil
}

// Reactivate lets a suspended user sign in again.
func (s *Service) Reactivate(ctx context.Context, a Actor, id uuid.UUID) error {
	if _, err := s.target(ctx, a, id); err != nil {
		return err
	}
	n, err := s.db.User.Update().Where(user.ID(id), user.StatusEQ(user.StatusSuspended)).
		SetStatus(user.StatusActive).ClearSuspendedAt().SetSuspensionNote("").Save(ctx)
	if err != nil {
		return fmt.Errorf("admin: reactivate: %w", err)
	}
	if n == 0 {
		return Problem("This account isn't suspended.")
	}
	s.audit.Record(ctx, a.event("admin.user_reactivated", "user", id.String(), nil))
	return nil
}

// SetRole grants or revokes a role (admins only). An admin can't remove
// their own admin role, so there's always someone who can undo mistakes.
func (s *Service) SetRole(ctx context.Context, a Actor, id uuid.UUID, role string, grant bool) error {
	if !a.IsAdmin() {
		return ErrForbidden
	}
	if !slices.Contains(Roles, role) {
		return Problem("Unknown role.")
	}
	if id == a.UserID && role == "admin" && !grant {
		return Problem("You can't remove your own admin role.")
	}
	u, err := s.db.User.Get(ctx, id)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if u.Status != user.StatusActive {
		return Problem("Reactivate the account first.")
	}
	if grant {
		err = s.db.RoleAssignment.Create().SetUserID(id).SetRole(roleassignment.Role(role)).Exec(ctx)
		if ent.IsConstraintError(err) {
			return nil // already has it
		}
	} else {
		_, err = s.db.RoleAssignment.Delete().Where(roleassignment.UserID(id), roleassignment.RoleEQ(roleassignment.Role(role))).Exec(ctx)
	}
	if err != nil {
		return fmt.Errorf("admin: role: %w", err)
	}
	action := "admin.role_revoke"
	if grant {
		action = "admin.role_grant"
	}
	s.audit.Record(ctx, a.event(action, "user", id.String(), map[string]any{"role": role}))
	return nil
}

// ── Audit log ────────────────────────────────────────────────────────────

type AuditFilter struct {
	Action string // prefix, e.g. "admin." or "listing.removed"
	Actor  string // phone or user ID
	Target string // any target ID
	Before time.Time
}

// Audit is one page (newest first) of the audit log.
func (s *Service) Audit(ctx context.Context, f AuditFilter) ([]*ent.AuditEvent, error) {
	q := s.db.AuditEvent.Query()
	if a := strings.TrimSpace(f.Action); a != "" {
		q.Where(auditevent.ActionHasPrefix(a))
	}
	if t := strings.TrimSpace(f.Actor); t != "" {
		id, err := uuid.Parse(t)
		if err != nil {
			e164, perr := phone.NormalizeGhana(t)
			if perr != nil {
				return nil, nil
			}
			u, uerr := s.db.User.Query().Where(user.Phone(e164)).Only(ctx)
			if uerr != nil {
				return nil, nil
			}
			id = u.ID
		}
		q.Where(auditevent.ActorID(id))
	}
	if t := strings.TrimSpace(f.Target); t != "" {
		q.Where(auditevent.TargetID(t))
	}
	if !f.Before.IsZero() {
		q.Where(auditevent.CreatedAtLT(f.Before))
	}
	evs, err := q.Order(ent.Desc(auditevent.FieldCreatedAt)).Limit(PageSize).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("admin: audit: %w", err)
	}
	return evs, nil
}

// Names maps user IDs to "Name (024 XXX XX67)" for display.
func (s *Service) Names(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]*ent.User {
	out := map[uuid.UUID]*ent.User{}
	if len(ids) == 0 {
		return out
	}
	us, _ := s.db.User.Query().Where(user.IDIn(ids...)).All(ctx)
	for _, u := range us {
		out[u.ID] = u
	}
	return out
}

// ── Flagged messages ─────────────────────────────────────────────────────

func flaggedOpen() predicate.Message {
	return predicate.Message(func(sel *sql.Selector) {
		// Unflagged messages hold JSON null; CASE keeps jsonb_array_length off
		// scalars (AND doesn't short-circuit in SQL).
		f := sel.C(message.FieldFlags)
		sel.Where(sql.And(sql.ExprP("CASE WHEN jsonb_typeof("+f+") = 'array' THEN jsonb_array_length("+f+") ELSE 0 END > 0"),
			sql.IsNull(sel.C(message.FieldFlagsReviewedAt))))
	})
}

// Flagged lists messages the scam shield flagged that no one has looked at.
func (s *Service) Flagged(ctx context.Context) ([]*ent.Message, error) {
	ms, err := s.db.Message.Query().Where(flaggedOpen()).Order(ent.Asc(message.FieldID)).Limit(100).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("admin: flagged: %w", err)
	}
	return ms, nil
}

func (s *Service) FlaggedCount(ctx context.Context) int {
	n, _ := s.db.Message.Query().Where(flaggedOpen()).Count(ctx)
	return n
}

// ReviewFlag clears a flagged message from the queue.
func (s *Service) ReviewFlag(ctx context.Context, a Actor, id uuid.UUID) error {
	n, err := s.db.Message.Update().Where(message.ID(id), message.FlagsReviewedAtIsNil()).SetFlagsReviewedAt(s.now()).Save(ctx)
	if err != nil {
		return fmt.Errorf("admin: review flag: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	s.audit.Record(ctx, a.event("admin.flag_reviewed", "message", id.String(), nil))
	return nil
}

// ── Listings ─────────────────────────────────────────────────────────────

// RemoveListing takes a listing down and tells the lister why.
func (s *Service) RemoveListing(ctx context.Context, a Actor, id uuid.UUID, reason string) error {
	l, err := s.listings.Remove(ctx, listings.Actor{UserID: a.UserID, Roles: a.Roles, IP: a.IP, UserAgent: a.UserAgent}, id, reason)
	if errors.Is(err, listings.ErrNotFound) {
		return ErrNotFound
	}
	var v listings.ValidationError
	if errors.As(err, &v) {
		return Problem(v["reason"])
	}
	if err != nil {
		return err
	}
	title := l.Headline
	if title == "" {
		title = "your listing"
	}
	s.notify.Send(ctx, l.ListerID, notify.Note{Topic: "listings", Kind: "listing.removed", Title: "We took down " + title,
		Body: strings.TrimSpace(reason), URL: "/listings",
		SMS: "RentMap: we took down " + title + ". Reason: " + strings.TrimSpace(reason)})
	return nil
}
