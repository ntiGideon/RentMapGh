package listings

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/modules/mandates"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

// ListingPage is the public page, /l/{id}/{slug}. Old or missing slugs
// redirect to the canonical one, so shared links survive a headline edit.
func (h *Handler) ListingPage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	var viewer uuid.UUID
	if v := reqctx.CurrentViewer(r.Context()); v != nil {
		viewer = v.UserID
	}
	d, lister, err := h.svc.PublicListing(r.Context(), id, viewer)
	if h.notFound(w, r, err) {
		return
	}
	path := PublicPath(d)
	if r.URL.Path != path {
		http.Redirect(w, r, path, http.StatusMovedPermanently) //nolint:gosec // G710: built from the parsed UUID and our own slug
		return
	}

	authority := mandates.None
	if d.L.ListerKind == listing.ListerKindAgent && h.mandates != nil {
		m, err := h.mandates.Latest(r.Context(), d.L.ListerID, d.P.ID)
		if err != nil {
			slog.ErrorContext(r.Context(), "listing page: authority", "err", err)
		}
		authority = mandates.StateOf(m, h.mandates.Now())
	}
	v := publicView(d, lister, authority, viewer, reqctx.DataSaver(r.Context()), time.Now().UTC())
	v.PageURL = h.baseURL + path
	v.AreaURL = "/search"
	if d.P.ApproxLat != nil && d.P.ApproxLng != nil {
		c := BBox{MinLng: *d.P.ApproxLng - 0.02, MinLat: *d.P.ApproxLat - 0.015, MaxLng: *d.P.ApproxLng + 0.02, MaxLat: *d.P.ApproxLat + 0.015}
		v.AreaURL = "/search?bbox=" + c.String()
	}
	if d.L.Status == listing.StatusActive {
		sim, err := h.svc.Similar(r.Context(), d, 4)
		if err != nil {
			slog.ErrorContext(r.Context(), "listing page: similar", "err", err)
		}
		v.Similar = h.cards(r.Context(), sim)
	}
	v.ShareURL = shareURL(v, v.PageURL)

	m := layouts.Meta{
		Title: v.Headline + " in " + v.Area, Description: metaDescription(v), URL: v.PageURL,
		NoIndex: d.L.Status != listing.StatusActive,
		Styles:  []string{"vendor/maplibre-6.11.2/maplibre-gl.css"},
		Modules: []string{"js/listing-page.js"},
	}
	if len(v.Photos) > 0 {
		m.Image = h.baseURL + v.Photos[0].Medium
	}
	if viewer == uuid.Nil && d.L.Status == listing.StatusActive {
		w.Header().Set("Cache-Control", "public, max-age=60")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	w.Header().Add("Vary", "Cookie")
	render.Component(w, r, http.StatusOK, pages.ListingPage(m, v))
}

// metaDescription is the preview text on WhatsApp and search results:
// price, move-in total and the place, in one line.
func metaDescription(v pages.ListingPageView) string {
	s := v.UnitType + " in " + v.Area + ", " + v.City + "."
	if v.Price != "" {
		s = v.Price + " per " + v.Per + " · " + s
	}
	if v.MoveInTotal != "" {
		s += " " + v.MoveInTotal + " to move in, every fee listed."
	}
	if v.Distance != "" {
		s += " " + v.Distance + "."
	}
	return s
}
