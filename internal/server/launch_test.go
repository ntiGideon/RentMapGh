package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelpAndLegalPages(t *testing.T) {
	h, _ := newTestServer(t)
	for path, want := range map[string]string{
		"/how-we-verify": "“ID checked”", "/safety": "Never pay before you've seen the place",
		"/guidelines": "Never ask for money before a viewing", "/terms": "Terms of use", "/privacy": "Data Protection Act",
	} {
		rec := do(h, "GET", path, nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, path)
		body := rec.Body.String()
		assert.Contains(t, body, want, path)
		assert.Contains(t, body, `rel="canonical" href="http://example.test`+path+`"`, path)
		assert.NotContains(t, body, "@contact", path)
		if path != "/how-we-verify" {
			assert.Contains(t, body, "support contact — to be added before launch", path+": no SUPPORT_EMAIL yet")
		}
		legal := path == "/terms" || path == "/privacy" || path == "/guidelines"
		assert.Equal(t, legal, strings.Contains(body, "Draft for review"), path)
	}
	home := do(h, "GET", "/", nil, nil).Body.String()
	for _, l := range []string{`href="/how-we-verify"`, `href="/safety"`, `href="/terms"`, `href="/privacy"`} {
		assert.Contains(t, home, l, "footer")
	}
	sitemap := do(h, "GET", "/sitemap.xml", nil, nil).Body.String()
	assert.Contains(t, sitemap, "<loc>http://example.test/privacy</loc>")
}

func TestInstallableApp(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	rec := do(h, "GET", "/manifest.webmanifest", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/manifest+json", rec.Header().Get("Content-Type"))
	var m struct {
		Name, StartURL, Display string
		Icons                   []struct{ Src, Sizes, Purpose string }
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	assert.Equal(t, "standalone", m.Display)
	require.Len(t, m.Icons, 3)
	for _, ic := range m.Icons {
		r := do(h, "GET", ic.Src, nil, nil)
		assert.Equal(t, http.StatusOK, r.Code, ic.Src)
		assert.Equal(t, "image/png", r.Header().Get("Content-Type"), ic.Src)
	}
	assert.Contains(t, do(h, "GET", "/", nil, nil).Body.String(), `<link rel="manifest" href="/manifest.webmanifest">`)

	rec = do(h, "GET", "/sw.js", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/javascript")
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	sw := rec.Body.String()
	assert.Contains(t, sw, `const OFFLINE = "/offline"`)
	assert.Contains(t, sw, "/static/v/", "the shell is the versioned assets")
	assert.NotContains(t, sw, "__", "placeholders filled")

	// The offline page is cached for everyone: nothing about the viewer.
	b := signInAs(t, h, d, capture, "0244000111", "Kofi Mensah", "renter")
	off := b.do("GET", "/offline", nil, false).Body.String()
	assert.Contains(t, off, "You're offline")
	assert.NotContains(t, off, "Kofi")
	assert.NotContains(t, off, "/account")
}
