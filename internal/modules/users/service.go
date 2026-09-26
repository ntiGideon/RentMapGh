// Package users handles onboarding, roles and the account page.
package users

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/roleassignment"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/storage"
)

// SelfServiceRoles can be picked by users themselves. Staff roles
// (field_verifier, moderator, admin) are only granted by an admin.
var SelfServiceRoles = []string{"renter", "landlord", "agent"}

func isSelfService(role string) bool {
	for _, r := range SelfServiceRoles {
		if r == role {
			return true
		}
	}
	return false
}

// ValidationError maps form field → message.
type ValidationError map[string]string

func (v ValidationError) Error() string { return fmt.Sprintf("users: %d invalid field(s)", len(v)) }

type Service struct {
	db    *ent.Client
	audit *audit.Log
	files storage.Store // avatars (not encrypted: they're shown to other users)
	// purgeEvidence removes a user's verification evidence (set by the router
	// to verification.Service.PurgeUser; nil in tests that don't need it).
	purgeEvidence func(context.Context, uuid.UUID) error
}

func NewService(db *ent.Client, log *audit.Log, files storage.Store, purgeEvidence func(context.Context, uuid.UUID) error) *Service {
	return &Service{db: db, audit: log, files: files, purgeEvidence: purgeEvidence}
}

// Actor identifies who is acting, for the audit log.
type Actor struct {
	UserID    uuid.UUID
	IP        string
	UserAgent string
}

// Onboard stores the name and the chosen roles and marks onboarding done.
func (s *Service) Onboard(ctx context.Context, a Actor, name string, roles []string) error {
	name, verr := cleanName(name)
	roles = dedupe(roles)
	if len(roles) == 0 {
		verr = add(verr, "roles", "Choose at least one option.")
	}
	for _, r := range roles {
		if !isSelfService(r) {
			verr = add(verr, "roles", "Choose from the options shown.")
		}
	}
	if verr != nil {
		return verr
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("onboard: begin: %w", err)
	}
	if err := grant(ctx, tx, a.UserID, roles); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.User.UpdateOneID(a.UserID).SetName(name).SetOnboardedAt(time.Now().UTC()).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("onboard: update user: %w", err)
	}
	if err := audit.RecordTx(ctx, tx, audit.Event{Actor: &a.UserID, Action: audit.Onboarded, IP: a.IP, UserAgent: a.UserAgent,
		Meta: map[string]any{"roles": roles}}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("onboard: audit: %w", err)
	}
	return tx.Commit()
}

// AddRole grants one more self-service role. Adding a role already held is a no-op.
func (s *Service) AddRole(ctx context.Context, a Actor, role string) error {
	if !isSelfService(role) {
		return ValidationError{"role": "That role can't be added from here."}
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("add role: begin: %w", err)
	}
	if err := grant(ctx, tx, a.UserID, []string{role}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := audit.RecordTx(ctx, tx, audit.Event{Actor: &a.UserID, Action: audit.RolesAdded, IP: a.IP, UserAgent: a.UserAgent,
		Meta: map[string]any{"roles": []string{role}}}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("add role: audit: %w", err)
	}
	return tx.Commit()
}

// UpdateName changes the display name.
func (s *Service) UpdateName(ctx context.Context, a Actor, name string) error {
	name, verr := cleanName(name)
	if verr != nil {
		return verr
	}
	if err := s.db.User.UpdateOneID(a.UserID).SetName(name).Exec(ctx); err != nil {
		return fmt.Errorf("update name: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: audit.ProfileUpdated, IP: a.IP, UserAgent: a.UserAgent,
		Meta: map[string]any{"fields": []string{"name"}}})
	return nil
}

// Get loads a user with roles and business profiles.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*ent.User, error) {
	return s.db.User.Query().Where(user.ID(id)).WithRoles().WithLandlordProfile().WithAgentProfile().Only(ctx)
}

// grant adds the roles the user doesn't hold yet. The unique index on
// (user_id, role) backstops concurrent grants.
func grant(ctx context.Context, tx *ent.Tx, userID uuid.UUID, roles []string) error {
	held, err := tx.RoleAssignment.Query().Where(roleassignment.UserID(userID)).All(ctx)
	if err != nil {
		return fmt.Errorf("grant: load roles: %w", err)
	}
	has := map[string]bool{}
	for _, h := range held {
		has[string(h.Role)] = true
	}
	for _, r := range roles {
		if has[r] {
			continue
		}
		if err := tx.RoleAssignment.Create().SetUserID(userID).SetRole(roleassignment.Role(r)).Exec(ctx); err != nil {
			return fmt.Errorf("grant %s: %w", r, err)
		}
	}
	return nil
}

func cleanName(name string) (string, ValidationError) {
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > 80 {
		return name, ValidationError{"name": "Please keep your name under 80 characters."}
	}
	return name, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func add(v ValidationError, field, msg string) ValidationError {
	if v == nil {
		v = ValidationError{}
	}
	if _, ok := v[field]; !ok {
		v[field] = msg
	}
	return v
}
