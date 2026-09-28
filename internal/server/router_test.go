package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/config"
	"rentmapgh/internal/db"
	"rentmapgh/internal/modules/listings"
	"rentmapgh/internal/platform/sms"
	"rentmapgh/internal/platform/storage"
	"rentmapgh/internal/platform/video"
	"rentmapgh/web"
)

// newTestServer needs a migrated Postgres (TEST_DATABASE_URL); CI provides one.
func newTestServer(t *testing.T) (http.Handler, *db.DB) {
	h, d, _ := newTestServerSMS(t)
	return h, d
}

func newTestServerSMS(t *testing.T) (http.Handler, *db.DB, *sms.Capture) {
	t.Helper()
	h, d, capture, _ := newTestServerMedia(t)
	return h, d, capture
}

func newTestServerMedia(t *testing.T) (http.Handler, *db.DB, *sms.Capture, *storage.Memory) {
	t.Helper()
	h, d, capture, media, _ := newTestServerFull(t, false)
	return h, d, capture, media
}

// newTestServerFull optionally turns on walk-through videos (skipping the
// test without ffmpeg) and returns the listings service to drive its worker.
func newTestServerFull(t *testing.T, withVideo bool) (http.Handler, *db.DB, *sms.Capture, *storage.Memory, *listings.Service) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	require.NoError(t, db.Migrate(dsn))
	d, err := db.Open(ctx, dsn, 4)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	_, err = d.Ent.WaitlistEntry.Delete().Exec(ctx)
	require.NoError(t, err)
	_, err = d.SQL.ExecContext(ctx, "TRUNCATE duplicate_candidates, notifications, reports, messages, conversations, viewings, saved_listings, agent_mandates, listing_media, listing_terms, listing_stats, listings, units, properties, audit_events, sessions, role_assignments, otp_codes, verification_files, verifications, agent_profiles, landlord_profiles, users")
	require.NoError(t, err)

	cfg := config.Config{Env: config.EnvDevelopment, BaseURL: "http://example.test",
		AuthSecret: "test-secret-test-secret-test-secret", LocationSecret: "test-location-secret", SessionTTL: 24 * time.Hour, SMSDailyCap: 100, EvidenceRetention: 90 * 24 * time.Hour}
	capture := &sms.Capture{}
	media := storage.NewMemory()
	deps := Deps{Cfg: cfg, DB: d, Assets: web.NewAssets(false), SMS: capture, Files: storage.NewMemory(), Media: media}
	if withVideo {
		if _, err := video.Find("", ""); err != nil {
			t.Skip("ffmpeg/ffprobe not on PATH")
		}
		deps.Cfg.VideoEnabled, deps.Cfg.VideoInbox = true, t.TempDir()
		deps.Listings, err = NewListings(deps)
		require.NoError(t, err)
		require.True(t, deps.Listings.VideoEnabled())
	}
	return New(deps), d, capture, media, deps.Listings
}

func do(h http.Handler, method, target string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var htmxSameOrigin = map[string]string{"HX-Request": "true", "Sec-Fetch-Site": "same-origin"}

func TestHomeAndHeaders(t *testing.T) {
	h, _ := newTestServer(t)

	rec := do(h, http.MethodGet, "/", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<!doctype html>")
	assert.Contains(t, rec.Body.String(), `id="waitlist-form"`)
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "script-src 'self'")
	assert.NotContains(t, rec.Header().Get("Content-Security-Policy"), "unsafe")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	assert.Equal(t, http.StatusNotFound, do(h, http.MethodGet, "/does-not-exist", nil, nil).Code)
	assert.Equal(t, http.StatusOK, do(h, http.MethodGet, "/healthz", nil, nil).Code)
	assert.Equal(t, http.StatusOK, do(h, http.MethodGet, "/readyz", nil, nil).Code)
}

func TestWaitlistJoin(t *testing.T) {
	h, d := newTestServer(t)
	valid := url.Values{"phone": {"024 123 4567"}, "name": {"Ama"}, "role": {"renter"}, "area": {"ayeduase"}, "consent": {"1"}}

	// Validation error → 422 with the form fragment only (no layout).
	rec := do(h, http.MethodPost, "/waitlist", url.Values{"phone": {"123"}}, htmxSameOrigin)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.NotContains(t, rec.Body.String(), "<html")
	assert.Contains(t, rec.Body.String(), "aria-invalid")

	// Success → fragment with masked phone; stored as E.164.
	rec = do(h, http.MethodPost, "/waitlist", valid, htmxSameOrigin)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Akwaaba, Ama!")
	assert.Contains(t, rec.Body.String(), "+233 24 *** 4567")
	assert.NotContains(t, rec.Body.String(), "+233241234567", "full number never echoed")
	n, err := d.Ent.WaitlistEntry.Query().Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// Same phone in another format → "already", still one row.
	rec = do(h, http.MethodPost, "/waitlist", url.Values{"phone": {"+233241234567"}, "consent": {"1"}}, htmxSameOrigin)
	assert.Contains(t, rec.Body.String(), "already on the list")
	n, _ = d.Ent.WaitlistEntry.Query().Count(context.Background())
	assert.Equal(t, 1, n)

	// Cross-site POST is rejected (CSRF).
	rec = do(h, http.MethodPost, "/waitlist", valid, map[string]string{"Sec-Fetch-Site": "cross-site"})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// No-JS post → 303 with a flash cookie, phone kept out of the URL.
	rec = do(h, http.MethodPost, "/waitlist", url.Values{"phone": {"0501234567"}, "consent": {"1"}}, map[string]string{"Sec-Fetch-Site": "same-origin"})
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/#join", rec.Header().Get("Location"))
	require.NotEmpty(t, rec.Result().Cookies())
}
