package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/pages"
	"rentmapgh/web"
)

// Installable web app: a manifest, a service worker and an offline page.
//
// The service worker is deliberately small. It precaches the offline page
// and the app shell (CSS, core JS, font, icons), serves versioned static
// files cache-first, and falls back to the offline page when a page can't
// load. It never stores pages, API responses or listing photos, so nothing
// personal is left on a shared phone and data-saver users don't download
// more than they asked for.

func manifest(a *web.Assets) http.HandlerFunc {
	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose,omitempty"`
	}
	body, _ := json.Marshal(map[string]any{
		"name":             "RentMap Ghana",
		"short_name":       "RentMap",
		"description":      "Honest rentals on a map: verified rooms and homes around KNUST with the full move-in cost.",
		"id":               "/",
		"start_url":        "/search?utm_source=pwa",
		"scope":            "/",
		"display":          "standalone",
		"orientation":      "portrait",
		"background_color": "#25272C",
		"theme_color":      "#25272C",
		"lang":             "en-GH",
		"categories":       []string{"lifestyle", "utilities"},
		"icons": []icon{
			{Src: a.URL("img/icon-192.png"), Sizes: "192x192", Type: "image/png"},
			{Src: a.URL("img/icon-512.png"), Sizes: "512x512", Type: "image/png"},
			{Src: a.URL("img/icon-maskable-512.png"), Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
		},
		"shortcuts": []map[string]string{
			{"name": "Find a place", "url": "/search"},
			{"name": "Saved places", "url": "/saved"},
			{"name": "Messages", "url": "/messages"},
		},
	})
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	}
}

const swTemplate = `// RentMap service worker (generated; see internal/server/pwa.go).
const CACHE = "rentmap-__VERSION__";
const OFFLINE = "/offline";
const SHELL = __SHELL__;

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll([OFFLINE, ...SHELL])).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k.startsWith("rentmap-") && k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // Pages: always the network; the offline page only when it fails.
  if (req.mode === "navigate") {
    e.respondWith(fetch(req).catch(() => caches.match(OFFLINE)));
    return;
  }
  // Versioned static files never change: cache first.
  if (url.pathname.startsWith("/static/v/") || url.pathname.startsWith("/static/fonts/")) {
    e.respondWith(
      caches.match(req).then((hit) => hit || fetch(req).then((res) => {
        if (res.ok) {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(req, copy));
        }
        return res;
      }))
    );
  }
  // Everything else (htmx, events, media) goes straight to the network.
});
`

func serviceWorker(a *web.Assets) http.HandlerFunc {
	shell := []string{a.URL("css/app.css"), a.URL("js/app.js"), a.URL("js/theme.js"), a.URL("vendor/htmx-2.0.11.min.js"),
		a.URL("img/favicon.svg"), a.URL("img/icon-192.png"), "/static/fonts/inter-latin-var.woff2"}
	list, _ := json.Marshal(shell)
	// The cache name follows the CSS version, so a deploy replaces the shell.
	version := strings.Trim(strings.TrimPrefix(a.URL("css/app.css"), web.Prefix+"v/"), "/")
	if i := strings.IndexByte(version, '/'); i > 0 {
		version = version[:i]
	}
	body := strings.NewReplacer("__VERSION__", version, "__SHELL__", string(list)).Replace(swTemplate)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache") // browsers check for a new worker on every visit
		_, _ = w.Write([]byte(body))
	}
}

func offlinePage(w http.ResponseWriter, r *http.Request) {
	render.Component(w, r, http.StatusOK, pages.Offline())
}
