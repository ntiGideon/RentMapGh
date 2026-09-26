package middleware

import (
	"net/http"
	"strings"
)

// Security sets a strict CSP plus the usual hardening headers. No inline
// scripts or styles and no eval (htmx runs with allowEval=false), so the policy
// is static and responses stay cacheable at the edge.
func Security(https bool) func(http.Handler) http.Handler {
	directives := []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self' data: blob: https://tiles.openfreemap.org",
		"font-src 'self'",
		// Map tiles, glyphs and sprites (OpenFreeMap, no API key).
		"connect-src 'self' https://tiles.openfreemap.org",
		"worker-src 'self'", // MapLibre's worker is a same-origin module
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}
	if https {
		directives = append(directives, "upgrade-insecure-requests")
	}
	csp := strings.Join(directives, "; ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Permissions-Policy", "geolocation=(self), camera=(self), microphone=(), payment=(), usb=()")
			if https {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
