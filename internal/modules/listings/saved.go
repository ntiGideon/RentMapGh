package listings

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/savedlisting"
	"rentmapgh/internal/server/reqctx"
)

// Saved places live in a signed cookie, so renters can shortlist without an
// account (ProjectRequirement §6.6). The value is the IDs, 16 bytes each,
// newest first, plus a truncated HMAC so nobody can plant IDs in it.

const (
	SavedCookie = "saved"
	MaxSaved    = 60 // ~1.3 KB of cookie
	savedTTL    = 365 * 24 * time.Hour
)

type savedCodec struct {
	secret []byte
	secure bool
}

func (c savedCodec) sign(payload string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte("saved:"))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// read returns the saved IDs (empty on a missing or tampered cookie).
func (c savedCodec) read(r *http.Request) []uuid.UUID {
	ck, err := r.Cookie(SavedCookie)
	if err != nil {
		return nil
	}
	payload, tag, ok := strings.Cut(ck.Value, ".")
	if !ok || !hmac.Equal([]byte(tag), []byte(c.sign(payload))) {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(raw)%16 != 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(raw)/16)
	for i := 0; i+16 <= len(raw) && len(ids) < MaxSaved; i += 16 {
		ids = append(ids, uuid.UUID(raw[i:i+16]))
	}
	return ids
}

func (c savedCodec) write(w http.ResponseWriter, ids []uuid.UUID) {
	if len(ids) > MaxSaved {
		ids = ids[:MaxSaved]
	}
	raw := make([]byte, 0, len(ids)*16)
	for _, id := range ids {
		raw = append(raw, id[:]...)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{Name: SavedCookie, Value: payload + "." + c.sign(payload), Path: "/",
		MaxAge: int(savedTTL.Seconds()), HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode})
}

// toggle adds id at the front, or removes it; it reports the new state.
func toggleSaved(ids []uuid.UUID, id uuid.UUID) ([]uuid.UUID, bool) {
	if i := slices.Index(ids, id); i >= 0 {
		return slices.Delete(slices.Clone(ids), i, i+1), false
	}
	return append([]uuid.UUID{id}, ids...), true
}

// ── Signed-in renters ────────────────────────────────────────────────────
// Saves live in saved_listings. The first request after sign-in that still
// carries the cookie moves its IDs into the account and clears it
// (ProjectRequirement Phase 4: "anonymous favourites merge").

// SavedIDs lists a user's saved listings, newest first.
func (s *Service) SavedIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.db.SavedListing.Query().Where(savedlisting.UserID(userID)).
		Order(ent.Desc(savedlisting.FieldCreatedAt)).Limit(MaxSaved * 2).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("saved: %w", err)
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ListingID
	}
	return ids, nil
}

// MergeSaved adds ids (oldest last) to the user's saves, skipping ones
// already there.
func (s *Service) MergeSaved(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) error {
	now := s.now()
	for i := len(ids) - 1; i >= 0; i-- { // oldest first, so the newest gets the latest time
		err := s.db.SavedListing.Create().SetUserID(userID).SetListingID(ids[i]).
			SetCreatedAt(now.Add(time.Duration(len(ids)-i)*time.Millisecond)).
			OnConflictColumns(savedlisting.FieldUserID, savedlisting.FieldListingID).DoNothing().Exec(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("saved: merge: %w", err)
		}
	}
	return nil
}

// ToggleSavedFor saves or un-saves a listing for a user and reports the new state.
func (s *Service) ToggleSavedFor(ctx context.Context, userID, listingID uuid.UUID) (bool, error) {
	n, err := s.db.SavedListing.Delete().Where(savedlisting.UserID(userID), savedlisting.ListingID(listingID)).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("saved: toggle: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	if err := s.MergeSaved(ctx, userID, []uuid.UUID{listingID}); err != nil {
		return false, err
	}
	return true, nil
}

// savedFor returns the viewer's saved IDs: the account's when signed in
// (merging and clearing any cookie first), else the cookie's.
func (h *Handler) savedFor(w http.ResponseWriter, r *http.Request) []uuid.UUID {
	v := reqctx.CurrentViewer(r.Context())
	if v == nil {
		return h.saved.read(r)
	}
	if ids := h.saved.read(r); len(ids) > 0 {
		if err := h.svc.MergeSaved(r.Context(), v.UserID, ids); err != nil {
			slog.WarnContext(r.Context(), "saved: merge", "err", err)
		} else {
			http.SetCookie(w, &http.Cookie{Name: SavedCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: h.saved.secure, SameSite: http.SameSiteLaxMode})
		}
	}
	ids, err := h.svc.SavedIDs(r.Context(), v.UserID)
	if err != nil {
		slog.WarnContext(r.Context(), "saved", "err", err)
	}
	return ids
}
