package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssets(t *testing.T) {
	a := NewAssets(false)
	h := a.Handler()

	u := a.URL("js/app.js")
	require.Regexp(t, regexp.MustCompile(`^/static/v/[0-9a-f]{10}/js/app\.js$`), u)
	assert.Equal(t, u, a.URL("js/app.js"), "stable across calls")

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	rec := get(u)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Cache-Control"), "immutable")

	rec = get("/static/v/0000000000/js/app.js") // stale hash still serves, but not immutable
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"))

	assert.Equal(t, http.StatusNotFound, get("/static/js/").Code, "no directory listings")
	assert.Equal(t, http.StatusNotFound, get("/static/nope.js").Code)
	assert.Equal(t, "/static/missing.css", a.URL("missing.css"))
}

func TestModuleMIMEType(t *testing.T) {
	a := NewAssets(false)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/static/vendor/maplibre-6.11.2/maplibre-gl.mjs", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("mjs served as %d %q, browsers won't run it", rec.Code, rec.Header().Get("Content-Type"))
	}
}
