// Package reqctx carries per-request values (data-saver flag, asset resolver,
// signed-in viewer) from middleware to handlers and templ views.
package reqctx

import (
	"context"
	"slices"

	"github.com/google/uuid"
)

type key int

const (
	dataSaverKey key = iota
	assetsKey
	viewerKey
	adminCountsKey
)

type AssetResolver interface{ URL(name string) string }

func WithDataSaver(ctx context.Context, v bool) context.Context {
	return context.WithValue(ctx, dataSaverKey, v)
}
func DataSaver(ctx context.Context) bool { b, _ := ctx.Value(dataSaverKey).(bool); return b }

func WithAssets(ctx context.Context, a AssetResolver) context.Context {
	return context.WithValue(ctx, assetsKey, a)
}

// Asset returns the fingerprinted URL for a static file.
func Asset(ctx context.Context, name string) string {
	if a, ok := ctx.Value(assetsKey).(AssetResolver); ok {
		return a.URL(name)
	}
	return "/static/" + name
}

// Viewer is the signed-in user as seen by the request. A nil *Viewer means
// anonymous.
type Viewer struct {
	UserID        uuid.UUID
	SessionID     uuid.UUID
	Phone         string // E.164
	Name          string
	Roles         []string
	PhoneVerified bool
	Onboarded     bool
	// IdentityVerified: Ghana Card + selfie approved by a moderator.
	IdentityVerified bool
	// LicenseVerified: agent licence approved by a moderator.
	LicenseVerified bool
	AvatarURL       string // "" → show the initial
	// ViewingAs is the admin behind a read-only "view as" session (support),
	// uuid.Nil for the user's own sessions.
	ViewingAs uuid.UUID
}

// IsViewAs reports a support session: an admin seeing the site as this user.
func (v *Viewer) IsViewAs() bool { return v != nil && v.ViewingAs != uuid.Nil }

func (v *Viewer) Has(role string) bool { return v != nil && slices.Contains(v.Roles, role) }

// HasAny reports whether the viewer holds at least one of roles.
func (v *Viewer) HasAny(roles ...string) bool {
	for _, r := range roles {
		if v.Has(r) {
			return true
		}
	}
	return false
}

// DisplayName is the first name, or a neutral fallback.
func (v *Viewer) DisplayName() string {
	if v == nil || v.Name == "" {
		return "there"
	}
	return v.Name
}

// Initial is the avatar letter.
func (v *Viewer) Initial() string {
	if v == nil || v.Name == "" {
		return "R"
	}
	for _, r := range v.Name {
		return string(r)
	}
	return "R"
}

func WithViewer(ctx context.Context, v *Viewer) context.Context {
	return context.WithValue(ctx, viewerKey, v)
}

// CurrentViewer returns the signed-in viewer, or nil.
func CurrentViewer(ctx context.Context) *Viewer { v, _ := ctx.Value(viewerKey).(*Viewer); return v }

// AdminCounts are the queue badges in the back-office nav.
type AdminCounts struct{ Verifications, Listings, Reports, Flagged, Duplicates int }

func WithAdminCounts(ctx context.Context, c AdminCounts) context.Context {
	return context.WithValue(ctx, adminCountsKey, c)
}

func CurrentAdminCounts(ctx context.Context) AdminCounts {
	c, _ := ctx.Value(adminCountsKey).(AdminCounts)
	return c
}

// NavCounts are the badges in the site header for a signed-in user.
type NavCounts struct{ Messages, Viewings, Notifications int }

type navCountsKey struct{}

func WithNavCounts(ctx context.Context, c NavCounts) context.Context {
	return context.WithValue(ctx, navCountsKey{}, c)
}

func CurrentNavCounts(ctx context.Context) NavCounts {
	c, _ := ctx.Value(navCountsKey{}).(NavCounts)
	return c
}
