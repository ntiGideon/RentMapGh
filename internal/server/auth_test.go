package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/ent/auditevent"
	"rentmapgh/internal/ent/session"
	"rentmapgh/internal/ent/user"
)

// browser keeps cookies between requests, like a real client.
type browser struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]*http.Cookie
}

func newBrowser(t *testing.T, h http.Handler) *browser {
	return &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
}

func (b *browser) do(method, target string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/131.0 Mobile Safari/537.36")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return rec
}

var codeRe = regexp.MustCompile(`code is (\d{6})`)

func TestPhoneOTPSignInFlow(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	b := newBrowser(t, h)

	rec := b.do(http.MethodGet, "/login", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Send code")

	// Invalid number → 422, nothing sent.
	rec = b.do(http.MethodPost, "/login", url.Values{"phone": {"12345"}}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Empty(t, capture.Messages())

	// Valid number → code step, one SMS with a 6-digit code and the WebOTP line.
	rec = b.do(http.MethodPost, "/login", url.Values{"phone": {"024 123 4567"}, "next": {"/account"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Enter your code")
	assert.Contains(t, rec.Body.String(), "+233 24 *** 4567")
	assert.NotContains(t, rec.Body.String(), "+233241234567", "full number never rendered")
	assert.Equal(t, "/login/verify?next=%2Faccount", rec.Header().Get("HX-Push-Url"))
	msg := capture.Last()
	assert.Equal(t, "+233241234567", msg.To)
	m := codeRe.FindStringSubmatch(msg.Body)
	require.Len(t, m, 2, msg.Body)
	code := m[1]
	assert.Contains(t, msg.Body, "@example.test #"+code)

	// Asking again inside the cooldown doesn't send a second SMS.
	rec = b.do(http.MethodPost, "/login", url.Values{"phone": {"0241234567"}}, true)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, capture.Messages(), 1)
	rec = b.do(http.MethodPost, "/login/resend", url.Values{}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "Please wait")

	// Wrong code → 422 with attempts left.
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	rec = b.do(http.MethodPost, "/login/verify", url.Values{"code": {wrong}}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "4 attempts left")

	// Right code (typed with a space) → signed in, new user sent to onboarding.
	rec = b.do(http.MethodPost, "/login/verify", url.Values{"code": {code[:3] + " " + code[3:]}, "next": {"/account"}}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "/onboarding?next=%2Faccount", rec.Header().Get("HX-Redirect"))
	require.Contains(t, b.cookies, "rm_session")
	assert.NotContains(t, b.cookies, "rm_otp", "pending cookie cleared")
	firstToken := b.cookies["rm_session"].Value

	u, err := d.Ent.User.Query().Where(user.Phone("+233241234567")).Only(ctx)
	require.NoError(t, err)
	assert.NotNil(t, u.PhoneVerifiedAt)
	assert.Nil(t, u.OnboardedAt)

	// The code is single-use.
	rec = b.do(http.MethodPost, "/login/verify", url.Values{"code": {code}}, true)
	assert.Equal(t, "/login", rec.Header().Get("HX-Redirect"), "no pending phone any more")

	// Not onboarded yet → account redirects to onboarding.
	rec = b.do(http.MethodGet, "/account", nil, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/onboarding?next=%2Faccount", rec.Header().Get("Location"))

	// Onboarding validates, then stores roles and rotates the session token.
	rec = b.do(http.MethodPost, "/onboarding", url.Values{"name": {"Kofi"}, "roles": {"admin"}}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "staff roles can't be self-assigned")
	rec = b.do(http.MethodPost, "/onboarding", url.Values{"name": {" Kofi "}, "roles": {"landlord", "renter"}, "next": {"/account"}}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "/account", rec.Header().Get("HX-Redirect"))
	assert.NotEqual(t, firstToken, b.cookies["rm_session"].Value, "token rotated on role change")

	rec = b.do(http.MethodGet, "/account", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Akwaaba, Kofi")
	assert.Contains(t, body, "Landlord")
	assert.Contains(t, body, "Phone verified")
	assert.Contains(t, body, "Chrome on Android")
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))

	// The pre-rotation token no longer works.
	old := newBrowser(t, h)
	old.cookies["rm_session"] = &http.Cookie{Name: "rm_session", Value: firstToken}
	rec = old.do(http.MethodGet, "/account", nil, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/login?next=%2Faccount", rec.Header().Get("Location"))

	// Audit trail.
	for _, action := range []string{"auth.otp_sent", "auth.otp_failed", "auth.signup", "user.onboarded"} {
		n, err := d.Ent.AuditEvent.Query().Where(auditevent.Action(action)).Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, n, action)
	}

	// Logout revokes the session.
	rec = b.do(http.MethodPost, "/logout", url.Values{}, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.NotContains(t, b.cookies, "rm_session")
	live, err := d.Ent.Session.Query().Where(session.UserID(u.ID), session.RevokedAtIsNil()).Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, live)
}

func TestSessionsDevicesAndLogOutEverywhere(t *testing.T) {
	h, d, capture := newTestServerSMS(t)

	signIn := func() *browser {
		b := newBrowser(t, h)
		b.do(http.MethodPost, "/login", url.Values{"phone": {"0501234567"}}, true)
		code := codeRe.FindStringSubmatch(capture.Last().Body)[1]
		rec := b.do(http.MethodPost, "/login/verify", url.Values{"code": {code}}, true)
		require.NotEmpty(t, rec.Header().Get("HX-Redirect"))
		return b
	}

	phone := signIn()
	rec := phone.do(http.MethodPost, "/onboarding", url.Values{"roles": {"renter"}}, true)
	require.Equal(t, "/account", rec.Header().Get("HX-Redirect"))

	// A second device needs a fresh code, past the resend cooldown.
	_, err := d.SQL.ExecContext(context.Background(), "UPDATE otp_codes SET created_at = created_at - interval '2 minutes'")
	require.NoError(t, err)
	laptop := signIn()

	rec = phone.do(http.MethodGet, "/account", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Log out of all other devices")

	rec = phone.do(http.MethodPost, "/account/sessions/revoke-others", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "All other devices have been logged out.")

	rec = laptop.do(http.MethodGet, "/account", nil, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code, "other device signed out")
	rec = phone.do(http.MethodGet, "/account", nil, false)
	assert.Equal(t, http.StatusOK, rec.Code, "this device still signed in")
}

func TestRequireAuthAndOpenRedirect(t *testing.T) {
	h, _ := newTestServer(t)
	b := newBrowser(t, h)

	rec := b.do(http.MethodGet, "/account", nil, true)
	assert.Equal(t, "/login?next=%2Faccount", rec.Header().Get("HX-Redirect"))

	// A hostile ?next= is dropped from the form.
	rec = b.do(http.MethodGet, "/login?next=//evil.example", nil, false)
	assert.NotContains(t, rec.Body.String(), "evil.example")

	// The code step without a pending phone goes back to the phone step.
	rec = b.do(http.MethodGet, "/login/verify", nil, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/login", rec.Header().Get("Location"))
}
