package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/session"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/server/reqctx"
)

// touchEvery limits last-seen writes to one per device per few minutes.
const touchEvery = 5 * time.Minute

var ErrNoSession = errors.New("auth: no such session")

// Sessions stores one row per signed-in device. The cookie holds a random
// token; the database holds only its SHA-256.
type Sessions struct {
	db  *ent.Client
	ttl time.Duration
	now func() time.Time
}

func NewSessions(db *ent.Client, ttl time.Duration) *Sessions {
	return &Sessions{db: db, ttl: ttl, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Sessions) TTL() time.Duration { return s.ttl }

// Create starts a session for userID and returns the cookie token.
func (s *Sessions) Create(ctx context.Context, userID uuid.UUID, userAgent, ip string) (string, *ent.Session, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", nil, err
	}
	now := s.now()
	row, err := s.db.Session.Create().
		SetUserID(userID).
		SetTokenHash(hash).
		SetUserAgent(clip(userAgent, 300)).
		SetIP(clip(ip, 64)).
		SetLastSeenAt(now).
		SetExpiresAt(now.Add(s.ttl)).
		Save(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("session: create: %w", err)
	}
	return token, row, nil
}

// Resolve maps a cookie token to a Viewer. Revoked, expired and suspended
// sessions resolve to ErrNoSession. It also slides the expiry forward.
func (s *Sessions) Resolve(ctx context.Context, token string) (*reqctx.Viewer, error) {
	if token == "" || len(token) > 100 {
		return nil, ErrNoSession
	}
	now := s.now()
	row, err := s.db.Session.Query().
		Where(session.TokenHash(hashToken(token)), session.RevokedAtIsNil(), session.ExpiresAtGT(now)).
		WithUser(func(q *ent.UserQuery) { q.WithRoles() }).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("session: resolve: %w", err)
	}
	u := row.Edges.User
	if u == nil || u.Status != user.StatusActive {
		return nil, ErrNoSession
	}

	if now.Sub(row.LastSeenAt) > touchEvery {
		// Best effort: a failed touch must not sign the user out.
		_ = s.db.Session.UpdateOneID(row.ID).SetLastSeenAt(now).SetExpiresAt(now.Add(s.ttl)).Exec(ctx)
		_ = s.db.User.UpdateOneID(u.ID).SetLastSeenAt(now).Exec(ctx)
	}
	return ViewerFor(u, row.ID), nil
}

// ViewerFor builds a Viewer from a user loaded with its roles.
func ViewerFor(u *ent.User, sessionID uuid.UUID) *reqctx.Viewer {
	v := &reqctx.Viewer{
		UserID:        u.ID,
		SessionID:     sessionID,
		Phone:         deref(u.Phone),
		Name:          u.Name,
		PhoneVerified: u.PhoneVerifiedAt != nil,
		Onboarded:     u.OnboardedAt != nil,

		IdentityVerified: u.IdentityVerifiedAt != nil,
		LicenseVerified:  u.LicenseVerifiedAt != nil,
		AvatarURL:        AvatarURL(u),
	}
	for _, r := range u.Edges.Roles {
		v.Roles = append(v.Roles, string(r.Role))
	}
	return v
}

// Rotate replaces the token of an existing session (after a privilege change)
// so a token captured earlier stops working.
func (s *Sessions) Rotate(ctx context.Context, sessionID uuid.UUID) (string, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	n, err := s.db.Session.Update().
		Where(session.ID(sessionID), session.RevokedAtIsNil()).
		SetTokenHash(hash).
		Save(ctx)
	if err != nil {
		return "", fmt.Errorf("session: rotate: %w", err)
	}
	if n == 0 {
		return "", ErrNoSession
	}
	return token, nil
}

// Revoke ends one of userID's sessions.
func (s *Sessions) Revoke(ctx context.Context, userID, sessionID uuid.UUID) error {
	n, err := s.db.Session.Update().
		Where(session.ID(sessionID), session.UserID(userID), session.RevokedAtIsNil()).
		SetRevokedAt(s.now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("session: revoke: %w", err)
	}
	if n == 0 {
		return ErrNoSession
	}
	return nil
}

// RevokeOthers ends every session of userID except keep ("log out everywhere else").
func (s *Sessions) RevokeOthers(ctx context.Context, userID, keep uuid.UUID) (int, error) {
	n, err := s.db.Session.Update().
		Where(session.UserID(userID), session.IDNEQ(keep), session.RevokedAtIsNil()).
		SetRevokedAt(s.now()).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("session: revoke others: %w", err)
	}
	return n, nil
}

// Active lists userID's live sessions, most recently used first.
func (s *Sessions) Active(ctx context.Context, userID uuid.UUID) ([]*ent.Session, error) {
	return s.db.Session.Query().
		Where(session.UserID(userID), session.RevokedAtIsNil(), session.ExpiresAtGT(s.now())).
		Order(ent.Desc(session.FieldLastSeenAt)).
		Limit(50).
		All(ctx)
}

// AvatarURL is the cache-busting URL of u's photo, or "".
func AvatarURL(u *ent.User) string {
	if u.AvatarKey == "" {
		return ""
	}
	return "/u/" + u.ID.String() + "/avatar.jpg?v=" + AvatarVersion(u.AvatarKey)
}

// AvatarVersion is the ?v= value for a stored avatar key.
func AvatarVersion(key string) string {
	sum := sha256.Sum256([]byte(key))
	return base64.RawURLEncoding.EncodeToString(sum[:6])
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func newToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("session: random: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
