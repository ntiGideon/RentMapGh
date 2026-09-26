// Package web holds the static assets (built CSS, JS, fonts, icons) and serves
// them with content-hashed URLs so browsers and Cloudflare can cache forever.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
)

//go:embed all:static
var embedded embed.FS

// Pin MIME types for what we serve. Go consults the OS table first (the
// Windows registry maps .mjs to text/plain), and browsers refuse to run ES
// modules — like MapLibre — without a JavaScript type.
func init() {
	for ext, typ := range map[string]string{
		".mjs": "text/javascript; charset=utf-8", ".js": "text/javascript; charset=utf-8",
		".css": "text/css; charset=utf-8", ".svg": "image/svg+xml", ".woff2": "font/woff2",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}

// Prefix is the URL path static assets are mounted under.
const Prefix = "/static/"

// Assets resolves asset names to fingerprinted URLs and serves them.
//
//	a.URL("css/app.css") → "/static/v/3fa2b1c9d0/css/app.css"
type Assets struct {
	fsys  fs.FS
	cache bool

	mu     sync.RWMutex
	hashes map[string]string
}

// NewAssets serves the embedded files, or web/static from disk when fromDisk is
// set (dev: CSS/JS edits show up without rebuilding the binary).
func NewAssets(fromDisk bool) *Assets {
	var fsys fs.FS
	if fromDisk {
		fsys = os.DirFS("web/static")
	} else {
		fsys, _ = fs.Sub(embedded, "static")
	}
	return &Assets{fsys: fsys, cache: !fromDisk, hashes: map[string]string{}}
}

// URL returns the cache-busting URL for name. Unknown files fall back to the
// plain path so a typo shows up as a 404 in the browser, not a panic.
func (a *Assets) URL(name string) string {
	h := a.hash(name)
	if h == "" {
		return Prefix + name
	}
	return Prefix + "v/" + h + "/" + name
}

func (a *Assets) hash(name string) string {
	if a.cache {
		a.mu.RLock()
		h, ok := a.hashes[name]
		a.mu.RUnlock()
		if ok {
			return h
		}
	}
	b, err := fs.ReadFile(a.fsys, name)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:5])
	if a.cache {
		a.mu.Lock()
		a.hashes[name] = h
		a.mu.Unlock()
	}
	return h
}

// Handler serves files under Prefix. Fingerprinted URLs (and fonts/vendor
// files, whose names carry a version) are immutable; everything else gets a
// short cache.
func (a *Assets) Handler() http.Handler {
	files := http.FileServerFS(a.fsys)
	return http.StripPrefix(strings.TrimSuffix(Prefix, "/"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path // e.g. /v/3fa2b1c9d0/css/app.css
		immutable := false
		if rest, ok := strings.CutPrefix(p, "/v/"); ok {
			if i := strings.IndexByte(rest, '/'); i > 0 {
				p = rest[i:]
				immutable = rest[:i] == a.hash(strings.TrimPrefix(p, "/"))
			}
		}
		if strings.HasSuffix(p, "/") { // no directory listings
			http.NotFound(w, r)
			return
		}
		switch {
		case !a.cache:
			w.Header().Set("Cache-Control", "no-cache")
		case immutable || strings.HasPrefix(p, "/fonts/") || strings.HasPrefix(p, "/vendor/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = path.Clean(p)
		files.ServeHTTP(w, r2)
	}))
}
