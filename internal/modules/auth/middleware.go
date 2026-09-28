package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
)

// LoadViewer resolves the session cookie and puts the Viewer in the request
// context. Anonymous requests pass through untouched.
func (h *Handler) LoadViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(SessionCookie)
		if err != nil || ck.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		v, err := h.sessions.Resolve(r.Context(), ck.Value)
		switch {
		case errors.Is(err, ErrNoSession):
			h.cookies.clearSession(w)
			if h.restoreOwn(w, r) { // a view-as session ran out: back to the admin's own
				if v, err := h.sessions.Resolve(r.Context(), mustCookie(r, returnCookie)); err == nil {
					r = r.WithContext(reqctx.WithViewer(r.Context(), v))
				}
			}
		case err != nil:
			slog.ErrorContext(r.Context(), "auth: resolve session", "err", err)
		default:
			w.Header().Add("Vary", "Cookie")
			w.Header().Set("Cache-Control", "private, no-store")
			r = r.WithContext(reqctx.WithViewer(r.Context(), v))
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuth sends anonymous visitors to /login and back afterwards.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqctx.CurrentViewer(r.Context()) == nil {
			htmx.Redirect(w, r, LoginURL(returnPath(r)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireOnboarded sends signed-in users who haven't picked a role yet to
// onboarding. Use after RequireAuth.
func RequireOnboarded(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := reqctx.CurrentViewer(r.Context()); v != nil && !v.Onboarded {
			htmx.Redirect(w, r, "/onboarding?next="+url.QueryEscape(returnPath(r)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireRole allows viewers holding at least one of roles. Use after RequireAuth.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !reqctx.CurrentViewer(r.Context()).HasAny(roles...) {
				render.Forbidden(w, r, "This area isn't for your account",
					"You don't have access to this page. If you think that's wrong, contact support.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireVerified allows viewers whose identity ("identity") or agent licence
// ("license") has been approved; others are sent to the matching form.
// Use after RequireAuth. Phase 2 puts publishing listings behind it.
func RequireVerified(kind string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v := reqctx.CurrentViewer(r.Context())
			switch {
			case kind == "license" && !v.LicenseVerified:
				htmx.Redirect(w, r, "/verify/licence")
			case kind != "license" && !v.IdentityVerified:
				htmx.Redirect(w, r, "/verify/identity")
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

// LoginURL builds /login with a validated return path.
func LoginURL(next string) string {
	if next = SafeNext(next); next == "" || next == "/" {
		return "/login"
	}
	return "/login?next=" + url.QueryEscape(next)
}

// returnPath is where to come back to after signing in. For htmx requests
// that's the page the user is on, not the fragment endpoint.
func returnPath(r *http.Request) string {
	if cur := r.Header.Get("HX-Current-URL"); cur != "" {
		if u, err := url.Parse(cur); err == nil {
			return u.RequestURI()
		}
	}
	if r.Method != http.MethodGet {
		return "/"
	}
	return r.URL.RequestURI()
}
