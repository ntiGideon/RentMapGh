package users

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/agentprofile"
	"rentmapgh/internal/ent/landlordprofile"
	"rentmapgh/internal/ent/roleassignment"
	"rentmapgh/internal/ent/session"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/waitlist"
	"rentmapgh/internal/platform/imaging"
	"rentmapgh/internal/platform/storage"
)

// ── Notification preferences (skeleton: stored now, used by Phase 4) ─────

// Topic is something we may message a user about.
type Topic struct{ Key, Label, Hint string }

var Topics = []Topic{
	{"viewings", "Viewings", "Requests, confirmations and reminders"},
	{"messages", "Messages", "When someone writes to you and you haven't read it"},
	{"listings", "Your listings", "\"Still available?\" checks for places you list"},
	{"alerts", "New places", "Listings that match your saved searches"},
	{"news", "RentMap news", "Launch updates and occasional offers"},
}

// Channels a topic can use. Security codes and verification results always
// go by SMS and aren't configurable.
var Channels = []string{"sms", "whatsapp"}

// DefaultPrefs: transactional on, marketing off (opt-in under Act 843).
var DefaultPrefs = map[string]bool{
	"viewings.sms": true, "viewings.whatsapp": true,
	"messages.sms": true, "messages.whatsapp": true,
	"listings.sms": true, "listings.whatsapp": true,
	"alerts.sms": false, "alerts.whatsapp": true,
	"news.sms": false, "news.whatsapp": false,
}

// Prefs merges stored preferences over the defaults.
func Prefs(u *ent.User) map[string]bool {
	out := map[string]bool{}
	for k, v := range DefaultPrefs {
		out[k] = v
	}
	for k, v := range u.NotificationPrefs {
		if _, known := DefaultPrefs[k]; known {
			out[k] = v
		}
	}
	return out
}

// SetNotificationPrefs replaces the preferences with the ticked keys.
func (s *Service) SetNotificationPrefs(ctx context.Context, a Actor, on []string) error {
	prefs := map[string]bool{}
	for k := range DefaultPrefs {
		prefs[k] = false
	}
	for _, k := range on {
		if _, known := DefaultPrefs[k]; known {
			prefs[k] = true
		}
	}
	if err := s.db.User.UpdateOneID(a.UserID).SetNotificationPrefs(prefs).Exec(ctx); err != nil {
		return fmt.Errorf("prefs: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: audit.ProfileUpdated, IP: a.IP, UserAgent: a.UserAgent,
		Meta: map[string]any{"fields": []string{"notification_prefs"}}})
	return nil
}

// SetDataSaver stores the account-level data saver choice.
func (s *Service) SetDataSaver(ctx context.Context, userID uuid.UUID, on bool) error {
	return s.db.User.UpdateOneID(userID).SetDataSaver(on).Exec(ctx)
}

// ── Avatar ────────────────────────────────────────────────────────────────

const AvatarSize = 256

// SetAvatar crops the photo to a square, stores it and swaps it in.
func (s *Service) SetAvatar(ctx context.Context, a Actor, data []byte) error {
	img, err := imaging.Avatar(data, AvatarSize)
	if err != nil {
		for _, e := range []error{imaging.ErrUnsupported, imaging.ErrTooSmall, imaging.ErrCorrupt} {
			if errors.Is(err, e) {
				return ValidationError{"avatar": e.Error()}
			}
		}
		return ValidationError{"avatar": imaging.ErrCorrupt.Error()}
	}
	key := "avatars/" + a.UserID.String() + "/" + randHex(8) + ".jpg"
	if err := s.files.Put(ctx, key, img); err != nil {
		return fmt.Errorf("avatar: store: %w", err)
	}
	u, err := s.db.User.Get(ctx, a.UserID) // (a single-column select can't scan a NULL key)
	if err != nil {
		return fmt.Errorf("avatar: load: %w", err)
	}
	old := u.AvatarKey
	if err := s.db.User.UpdateOneID(a.UserID).SetAvatarKey(key).Exec(ctx); err != nil {
		_ = s.files.Delete(ctx, key)
		return fmt.Errorf("avatar: save: %w", err)
	}
	if old != "" {
		_ = s.files.Delete(ctx, old)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: audit.ProfileUpdated, IP: a.IP, UserAgent: a.UserAgent,
		Meta: map[string]any{"fields": []string{"avatar"}}})
	return nil
}

func (s *Service) RemoveAvatar(ctx context.Context, a Actor) error {
	u, err := s.db.User.Get(ctx, a.UserID) // (a single-column select can't scan a NULL key)
	if err != nil {
		return fmt.Errorf("avatar: load: %w", err)
	}
	old := u.AvatarKey
	if err := s.db.User.UpdateOneID(a.UserID).SetAvatarKey("").Exec(ctx); err != nil {
		return fmt.Errorf("avatar: clear: %w", err)
	}
	if old != "" {
		_ = s.files.Delete(ctx, old)
	}
	return nil
}

// Avatar returns the stored photo for a user, or storage.ErrNotFound.
func (s *Service) Avatar(ctx context.Context, userID uuid.UUID) (key string, data []byte, err error) {
	u, err := s.db.User.Query().Where(user.ID(userID), user.StatusEQ(user.StatusActive)).Select(user.FieldAvatarKey).Only(ctx)
	if ent.IsNotFound(err) || (err == nil && u.AvatarKey == "") {
		return "", nil, storage.ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	data, err = s.files.Get(ctx, u.AvatarKey)
	return u.AvatarKey, data, err
}

// ── Business profiles ─────────────────────────────────────────────────────

type LandlordInput struct{ DisplayName, Bio string }

func (s *Service) SaveLandlordProfile(ctx context.Context, a Actor, in LandlordInput) error {
	name := strings.Join(strings.Fields(in.DisplayName), " ")
	bio := strings.TrimSpace(in.Bio)
	errs := ValidationError{}
	if utf8.RuneCountInString(name) > 80 {
		errs["display_name"] = "Keep the name under 80 characters."
	}
	if utf8.RuneCountInString(bio) > 600 {
		errs["bio"] = "Keep this under 600 characters."
	}
	if len(errs) > 0 {
		return errs
	}
	err := s.db.LandlordProfile.Create().SetUserID(a.UserID).SetDisplayName(name).SetBio(bio).
		OnConflictColumns(landlordprofile.FieldUserID).
		Update(func(u *ent.LandlordProfileUpsert) { u.SetDisplayName(name).SetBio(bio).UpdateUpdatedAt() }).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("landlord profile: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: audit.ProfileUpdated, IP: a.IP, UserAgent: a.UserAgent,
		Meta: map[string]any{"fields": []string{"landlord_profile"}}})
	return nil
}

type AgentInput struct {
	AgencyName string
	Areas      []string
}

func (s *Service) SaveAgentProfile(ctx context.Context, a Actor, in AgentInput) error {
	agency := strings.Join(strings.Fields(in.AgencyName), " ")
	if utf8.RuneCountInString(agency) > 120 {
		return ValidationError{"agency_name": "Keep the agency name under 120 characters."}
	}
	var areas []string
	for _, slug := range dedupe(in.Areas) {
		if waitlist.AreaLabel(slug) != "" {
			areas = append(areas, slug)
		}
	}
	err := s.db.AgentProfile.Create().SetUserID(a.UserID).SetAgencyName(agency).SetServiceAreas(areas).
		OnConflictColumns(agentprofile.FieldUserID).
		Update(func(u *ent.AgentProfileUpsert) { u.SetAgencyName(agency).SetServiceAreas(areas).UpdateUpdatedAt() }).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("agent profile: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: audit.ProfileUpdated, IP: a.IP, UserAgent: a.UserAgent,
		Meta: map[string]any{"fields": []string{"agent_profile"}}})
	return nil
}

// ── Deletion ──────────────────────────────────────────────────────────────

// Delete anonymises the account (ProjectRequirement risk #3): the row stays
// so audit history, and later reviews and ledgers, keep their references, but
// every piece of personal data is removed and the phone number is freed.
func (s *Service) Delete(ctx context.Context, a Actor) error {
	u, err := s.db.User.Get(ctx, a.UserID)
	if err != nil {
		return fmt.Errorf("delete: load: %w", err)
	}
	if s.purgeEvidence != nil {
		if err := s.purgeEvidence(ctx, a.UserID); err != nil {
			return fmt.Errorf("delete: evidence: %w", err)
		}
	}

	now := time.Now().UTC()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("delete: begin: %w", err)
	}
	steps := []func() error{
		func() error {
			_, err := tx.Session.Update().Where(session.UserID(a.UserID), session.RevokedAtIsNil()).SetRevokedAt(now).Save(ctx)
			return err
		},
		func() error {
			_, err := tx.RoleAssignment.Delete().Where(roleassignment.UserID(a.UserID)).Exec(ctx)
			return err
		},
		func() error {
			_, err := tx.LandlordProfile.Delete().Where(landlordprofile.UserID(a.UserID)).Exec(ctx)
			return err
		},
		func() error {
			_, err := tx.AgentProfile.Delete().Where(agentprofile.UserID(a.UserID)).Exec(ctx)
			return err
		},
		func() error {
			return tx.User.UpdateOneID(a.UserID).
				ClearPhone().SetName("").SetAvatarKey("").ClearNotificationPrefs().
				SetStatus(user.StatusDeleted).SetDeletedAt(now).
				ClearPhoneVerifiedAt().ClearIdentityVerifiedAt().ClearLicenseVerifiedAt().
				Exec(ctx)
		},
		func() error {
			return audit.RecordTx(ctx, tx, audit.Event{Actor: &a.UserID, Action: "user.deleted", IP: a.IP, UserAgent: a.UserAgent})
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("delete: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete: commit: %w", err)
	}
	if u.AvatarKey != "" {
		if err := s.files.Delete(ctx, u.AvatarKey); err != nil {
			slog.WarnContext(ctx, "delete: avatar file", "err", err)
		}
	}
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
