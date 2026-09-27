package listings

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
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
