// Package render writes templ components and implements the handler rule:
// htmx requests get the fragment, everything else gets the full page.
package render

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/views/pages"
)

// Component writes c with the given status.
func Component(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		slog.ErrorContext(r.Context(), "render", "err", err, "path", r.URL.Path)
	}
}

// Page renders partial for htmx requests and full otherwise, so every URL
// works when opened directly or shared on WhatsApp.
func Page(w http.ResponseWriter, r *http.Request, status int, full, partial templ.Component) {
	w.Header().Add("Vary", "HX-Request")
	if htmx.IsPartial(r) {
		Component(w, r, status, partial)
		return
	}
	Component(w, r, status, full)
}

var errorCopy = map[int]pages.ErrorPage{
	http.StatusBadRequest:            {Title: "That didn't come through", Message: "Something was wrong with the form we received. Please go back and try again."},
	http.StatusRequestEntityTooLarge: {Title: "That file is too big", Message: "Photos must be under 12 MB each. Try taking the photo again, or pick a smaller one."},
	http.StatusNotFound:              {Title: "We can't find that page", Message: "The link may be old, or the listing may have been rented. Let's get you back on the map."},
	http.StatusForbidden:             {Title: "Your session expired", Message: "For your security, please go back, refresh the page and try again."},
	http.StatusMethodNotAllowed:      {Title: "That didn't work", Message: "This page can't handle that request."},
	http.StatusTooManyRequests:       {Title: "Slow down a little", Message: "Too many attempts from your connection. Please wait a minute and try again."},
	http.StatusInternalServerError:   {Title: "Something went wrong on our side", Message: "We've been notified. Please try again in a moment."},
}

// Forbidden is the "signed in, but not allowed" page (403 without the
// session-expired copy used for CSRF rejections).
func Forbidden(w http.ResponseWriter, r *http.Request, title, message string) {
	if htmx.IsPartial(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	Component(w, r, http.StatusForbidden, pages.Error(pages.ErrorPage{Status: http.StatusForbidden, Title: title, Message: message}))
}

// Error renders a friendly error page (or just the status for htmx requests,
// which the client turns into a toast).
func Error(w http.ResponseWriter, r *http.Request, status int) {
	if htmx.IsPartial(r) {
		w.WriteHeader(status)
		return
	}
	e, ok := errorCopy[status]
	if !ok {
		e = errorCopy[http.StatusInternalServerError]
	}
	e.Status = status
	Component(w, r, status, pages.Error(e))
}
