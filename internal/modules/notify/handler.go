package notify

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Page is /notifications: the latest 50, then everything is marked read.
func (h *Handler) Page(w http.ResponseWriter, r *http.Request) {
	v := reqctx.CurrentViewer(r.Context())
	ns, err := h.svc.List(r.Context(), v.UserID, 50)
	if err != nil {
		slog.ErrorContext(r.Context(), "notifications", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	var view pages.NotificationsView
	now := h.svc.now()
	for _, n := range ns {
		url := n.URL
		if url == "" {
			url = "/notifications"
		}
		view.Rows = append(view.Rows, pages.NotificationRow{Title: n.Title, Body: n.Body, URL: url, When: ago(n.CreatedAt, now), Unread: n.ReadAt == nil})
	}
	if err := h.svc.MarkAllRead(r.Context(), v.UserID); err != nil {
		slog.WarnContext(r.Context(), "notifications: read", "err", err)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	c := reqctx.CurrentNavCounts(r.Context())
	c.Notifications = 0 // just read them all
	r = r.WithContext(reqctx.WithNavCounts(r.Context(), c))
	render.Component(w, r, http.StatusOK, pages.Notifications(layouts.Meta{Title: "Notifications", NoIndex: true}, view))
}

func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return t.Format("Mon")
	}
	return t.Format("2 Jan")
}
