// Package htmx has helpers for reading htmx request headers and setting
// response headers.
package htmx

import (
	"encoding/json"
	"net/http"
)

// IsPartial reports whether the request wants an HTML fragment. Boosted
// navigations (hx-boost) expect a full page, so they don't count.
func IsPartial(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true"
}

// Toast fires the client-side `showToast` event (see web/static/js/app.js).
func Toast(w http.ResponseWriter, kind, message string) {
	Trigger(w, "showToast", map[string]string{"kind": kind, "message": message})
}

// Trigger sets HX-Trigger with a single event and JSON payload.
func Trigger(w http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(map[string]any{event: payload})
	if err != nil {
		return
	}
	w.Header().Set("HX-Trigger", string(b))
}

// Redirect navigates the browser: HX-Redirect for htmx requests (a plain 3xx
// would be followed by XHR and swapped into the page), 303 otherwise.
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}
