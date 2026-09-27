package listings

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

// ToggleSaved hearts or un-hearts a listing. htmx gets the new button and
// a toast; the no-script form goes back where it came from.
func (h *Handler) ToggleSaved(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	// Only public listings can be saved (no probing drafts).
	ok, err := h.svc.db.Listing.Query().Where(listing.ID(id), listing.StatusIn(publicStatuses...)).Exist(r.Context())
	if err != nil || !ok {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	ids, saved := toggleSaved(h.saved.read(r), id)
	if saved && len(ids) > MaxSaved {
		ids = ids[:MaxSaved] // the oldest falls off
	}
	h.saved.write(w, ids)
	back := safeBack(r.PostFormValue("back"))
	if !htmx.IsPartial(r) {
		redirect(w, r, back)
		return
	}
	msg := "Removed from saved"
	if saved {
		msg = "Saved. Find it any time under Saved."
	}
	htmx.Toast(w, "success", msg)
	w.Header().Set("Cache-Control", "no-store")
	render.Component(w, r, http.StatusOK, partials.SaveButton(id.String(), saved, back, r.PostFormValue("style") != "inline"))
}

// safeBack keeps redirects on this site.
func safeBack(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "/saved"
	}
	return p
}

// SavedPage lists the renter's saved places, newest first.
func (h *Handler) SavedPage(w http.ResponseWriter, r *http.Request) {
	ids := h.saved.read(r)
	items, err := h.svc.loadPublic(r.Context(), ids)
	if err != nil {
		slog.ErrorContext(r.Context(), "saved", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	var live, gone []*Item
	for _, d := range items {
		if d.L.Status == listing.StatusActive {
			live = append(live, d)
		} else if slices.Contains(publicStatuses, d.L.Status) {
			gone = append(gone, d)
		}
	}
	v := pages.SavedView{Cards: h.cards(r.Context(), live, Filter{}.Ref(), ids)}
	for _, d := range gone {
		v.Gone = append(v.Gone, pages.GoneItem{Title: Title(d), URL: PublicPath(d), Status: statusWord(d.L.Status)})
	}
	m := layouts.Meta{Title: "Saved places", NoIndex: true, Modules: []string{"js/compare.js"}}
	w.Header().Set("Cache-Control", "private, no-store")
	render.Component(w, r, http.StatusOK, pages.Saved(m, v))
}

func statusWord(s listing.Status) string {
	switch s {
	case listing.StatusRented:
		return "Rented"
	case listing.StatusPaused:
		return "Paused"
	case listing.StatusExpired:
		return "Not confirmed recently"
	}
	return ""
}
