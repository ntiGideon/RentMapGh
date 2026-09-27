package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent/roleassignment"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/platform/sms"
)

// upload sends a multipart form like a browser would.
func (b *browser) upload(target string, fields map[string]string, files map[string][]byte, htmx bool) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for k, data := range files {
		fw, _ := mw.CreateFormFile(k, k+".jpg")
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
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

func testJPEG(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 800, 520))
	for y := 0; y < 520; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{uint8(x / 4), uint8(y / 3), 140, 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

// signInAs runs the real OTP flow, onboards with the self-service roles and
// grants any staff roles directly.
func signInAs(t *testing.T, h http.Handler, d *db.DB, capture *sms.Capture, phone, name string, roles ...string) *browser {
	t.Helper()
	return signInWith(t, newBrowser(t, h), d, capture, phone, name, roles...)
}

// signInWith signs in on an existing browser (keeping its cookies).
func signInWith(t *testing.T, b *browser, d *db.DB, capture *sms.Capture, phone, name string, roles ...string) *browser {
	t.Helper()
	ctx := context.Background()
	_, err := d.SQL.ExecContext(ctx, "UPDATE otp_codes SET created_at = created_at - interval '2 minutes'")
	require.NoError(t, err)
	rec := b.do(http.MethodPost, "/login", url.Values{"phone": {phone}}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	code := codeRe.FindStringSubmatch(capture.Last().Body)[1]
	rec = b.do(http.MethodPost, "/login/verify", url.Values{"code": {code}}, true)
	require.NotEmpty(t, rec.Header().Get("HX-Redirect"))

	self := url.Values{"name": {name}}
	var staff []string
	for _, r := range roles {
		switch r {
		case "renter", "landlord", "agent":
			self.Add("roles", r)
		default:
			staff = append(staff, r)
		}
	}
	if len(self["roles"]) == 0 {
		self.Add("roles", "renter")
	}
	rec = b.do(http.MethodPost, "/onboarding", self, true)
	require.Equal(t, "/account", rec.Header().Get("HX-Redirect"), rec.Body.String())
	if len(staff) > 0 {
		u, err := d.Ent.User.Query().Where(user.Phone(normalize(phone))).Only(ctx)
		require.NoError(t, err)
		for _, r := range staff {
			require.NoError(t, d.Ent.RoleAssignment.Create().SetUserID(u.ID).SetRole(roleassignment.Role(r)).Exec(ctx))
		}
	}
	return b
}

func normalize(local string) string { return "+233" + local[1:] }

func TestAuthorizationMatrix(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	anon := newBrowser(t, h)
	renter := signInAs(t, h, d, capture, "0241111111", "Rita", "renter")
	landlord := signInAs(t, h, d, capture, "0242222222", "Lord", "landlord")
	agent := signInAs(t, h, d, capture, "0243333333", "Agnes", "agent")
	mod := signInAs(t, h, d, capture, "0244444444", "Mo", "renter", "moderator")

	type want int
	const (
		ok       want = http.StatusOK
		login    want = -1 // redirect to /login
		denied   want = http.StatusForbidden
		redirect want = http.StatusSeeOther
	)
	cases := []struct {
		name   string
		b      *browser
		method string
		path   string
		want   want
	}{
		{"anon account", anon, "GET", "/account", login},
		{"anon admin", anon, "GET", "/admin/verifications", login},
		{"anon verify", anon, "GET", "/verify/identity", login},
		{"renter admin", renter, "GET", "/admin/verifications", denied},
		{"landlord admin", landlord, "GET", "/admin/verifications", denied},
		{"agent admin", agent, "GET", "/admin/verifications", denied},
		{"moderator admin", mod, "GET", "/admin/verifications", ok},
		{"renter verify identity", renter, "GET", "/verify/identity", ok},
		{"renter licence form", renter, "GET", "/verify/licence", denied},
		{"agent licence form", agent, "GET", "/verify/licence", ok},
		{"renter agent profile", renter, "POST", "/account/agent-profile", denied},
		{"landlord agent profile", landlord, "POST", "/account/agent-profile", denied},
		{"agent agent profile", agent, "POST", "/account/agent-profile", redirect},
		{"renter landlord profile", renter, "POST", "/account/landlord-profile", denied},
		{"landlord landlord profile", landlord, "POST", "/account/landlord-profile", redirect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var form url.Values
			if tc.method == "POST" {
				form = url.Values{}
			}
			rec := tc.b.do(tc.method, tc.path, form, false)
			if tc.want == login {
				assert.Equal(t, http.StatusSeeOther, rec.Code)
				assert.Contains(t, rec.Header().Get("Location"), "/login")
				return
			}
			assert.Equal(t, int(tc.want), rec.Code)
		})
	}
}

func TestIdentityVerificationOverHTTP(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	landlord := signInAs(t, h, d, capture, "0242222222", "Kofi", "landlord")
	mod := signInAs(t, h, d, capture, "0244444444", "Mo", "renter", "moderator")
	photo := testJPEG(t)

	// Missing photos → 422 with field errors, nothing stored.
	rec := landlord.upload("/verify/identity", map[string]string{"card_number": "GHA-123456789-0", "consent": "1"}, nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "Add a photo of the front of your card")

	rec = landlord.upload("/verify/identity",
		map[string]string{"card_number": "gha 123456789 0", "consent": "1"},
		map[string][]byte{"id_front": photo, "id_back": photo, "selfie": photo}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "/account?submitted=identity", rec.Header().Get("HX-Redirect"))

	rec = landlord.do("GET", "/account?submitted=identity", nil, false)
	assert.Contains(t, rec.Body.String(), "Under review")
	assert.Contains(t, rec.Body.String(), "your ID is with our team")

	// Moderator queue → review page with the decrypted number and 3 photos.
	rec = mod.do("GET", "/admin/verifications", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Kofi")
	id := regexp.MustCompile(`/admin/verifications/([0-9a-f-]{36})"`).FindStringSubmatch(rec.Body.String())
	require.Len(t, id, 2)
	rec = mod.do("GET", "/admin/verifications/"+id[1], nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "GHA-123456789-0")
	files := regexp.MustCompile(`src="(/admin/verifications/[^"]+/files/[^"]+)"`).FindAllStringSubmatch(rec.Body.String(), -1)
	require.Len(t, files, 3)

	rec = mod.do("GET", files[0][1], nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))

	// The owner can't open evidence files (or the queue).
	assert.Equal(t, http.StatusForbidden, landlord.do("GET", files[0][1], nil, false).Code)

	// Approve → SMS + badge.
	rec = mod.do("POST", "/admin/verifications/"+id[1]+"/decision", url.Values{"decision": {"approve"}}, true)
	require.Equal(t, "/admin/verifications?done=approved", rec.Header().Get("HX-Redirect"), rec.Body.String())
	assert.Contains(t, capture.Last().Body, "identity is verified")

	rec = landlord.do("GET", "/account", nil, false)
	assert.Contains(t, rec.Body.String(), "Verified on")
	rec = landlord.do("GET", "/verify/identity", nil, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code, "already verified → back to account")
}

func TestAvatarAndAccountDeletion(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	b := signInAs(t, h, d, capture, "0245555555", "Esi", "landlord")

	rec := b.upload("/account/avatar", nil, map[string][]byte{"avatar": testJPEG(t)}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	rec = b.do("GET", "/account", nil, false)
	src := regexp.MustCompile(`src="(/u/[0-9a-f-]+/avatar\.jpg\?v=[^"]+)"`).FindStringSubmatch(rec.Body.String())
	require.Len(t, src, 2)
	img := newBrowser(t, h).do("GET", src[1], nil, false)
	require.Equal(t, http.StatusOK, img.Code)
	assert.Contains(t, img.Header().Get("Cache-Control"), "immutable")

	// Wrong confirmation keeps the account.
	rec = b.do("POST", "/account/delete", url.Values{"confirm": {"yes"}}, false)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = b.do("POST", "/account/delete", url.Values{"confirm": {"delete"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/account/deleted", rec.Header().Get("Location"))
	assert.NotContains(t, b.cookies, "rm_session")

	deleted, err := d.Ent.User.Query().Where(user.StatusEQ(user.StatusDeleted)).Only(ctx)
	require.NoError(t, err)
	assert.Nil(t, deleted.Phone, "number freed")
	assert.Empty(t, deleted.Name)
	assert.Empty(t, deleted.AvatarKey)
	assert.Equal(t, http.StatusNotFound, newBrowser(t, h).do("GET", src[1], nil, false).Code, "photo gone")

	// The same number can sign up again as a brand-new account.
	again := signInAs(t, h, d, capture, "0245555555", "Esi", "renter")
	fresh, err := d.Ent.User.Query().Where(user.Phone("+233245555555")).Only(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, deleted.ID, fresh.ID)
	assert.Equal(t, http.StatusOK, again.do("GET", "/account", nil, false).Code)
}
