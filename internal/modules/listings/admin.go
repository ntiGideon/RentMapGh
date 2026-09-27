package listings

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/modules/mandates"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

// AdminQueue lists listings waiting for review.
func (h *Handler) AdminQueue(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.Queue(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "admin listings: queue", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	q := partials.AdminListingQueue{}
	switch r.URL.Query().Get("done") {
	case "approve":
		q.Notice = "Approved — the listing is live."
	case "changes":
		q.Notice = "Sent back. The lister sees your note on their listing."
	}
	now := time.Now()
	for _, d := range items {
		row := partials.AdminListingRow{ID: d.L.ID.String(), Title: Title(d), Kind: kindLabel(string(d.L.ListerKind))}
		if n, ok := geo.NeighbourhoodBySlug(d.P.Neighbourhood); ok {
			row.Area = n.Label
		}
		if d.T.Rent != nil {
			row.Rent = d.T.Rent.String()
		}
		if d.L.SubmittedAt != nil {
			row.Submitted = ago(*d.L.SubmittedAt, now)
		}
		if u, err := h.svc.db.User.Get(r.Context(), d.L.ListerID); err == nil {
			row.Lister = orDash(u.Name)
		}
		q.Rows = append(q.Rows, row)
	}
	render.Component(w, r, http.StatusOK, pages.AdminListings(q))
}

// AdminReview shows one listing in full, exact pin included.
func (h *Handler) AdminReview(w http.ResponseWriter, r *http.Request) {
	h.renderReview(w, r, http.StatusOK, nil)
}

func (h *Handler) renderReview(w http.ResponseWriter, r *http.Request, status int, errs ValidationError) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	d, lister, err := h.svc.ForReview(r.Context(), id)
	if h.notFound(w, r, err) {
		return
	}
	v := partials.AdminListingView{
		ID: id.String(), Title: Title(d), Status: string(d.L.Status), Kind: kindLabel(string(d.L.ListerKind)),
		ListerName: lister.Name, ListerVerified: lister.IdentityVerifiedAt != nil,
		Headline: orDash(d.L.Headline), Description: d.L.Description, Landmark: d.P.Landmark,
		Sections: summary(d), MoveIn: moveInView(d.Terms()), Own: lister.ID == actor(r).UserID,
		Errors: errs, Note: r.PostFormValue("note"),
	}
	if lister.Phone != nil {
		v.ListerPhone = phone.Pretty(*lister.Phone)
	}
	v.ExactPin, v.ApproxPin = fmtPin(d.P.Lat, d.P.Lng), fmtPin(d.P.ApproxLat, d.P.ApproxLng)
	v.Photos = photosView(d, "").Tiles
	v.Video = videoView(d, true, reqctx.DataSaver(r.Context()), "")
	if d.L.ListerKind == listing.ListerKindAgent && h.mandates != nil {
		v.IsAgent = true
		if m, err := h.mandates.Latest(r.Context(), d.L.ListerID, d.P.ID); err == nil {
			v.Authority = string(mandates.StateOf(m, h.mandates.Now()))
			v.AuthorityReported = m != nil && m.Reported
		}
	}
	v.Video.Editable = false
	render.Component(w, r, status, pages.AdminListingReview(v))
}

func (h *Handler) AdminDecide(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	approve := r.PostFormValue("decision") == "approve"
	err := h.svc.Decide(r.Context(), actor(r), id, approve, r.PostFormValue("note"))
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.renderReview(w, r, http.StatusUnprocessableEntity, verr)
	case h.notFound(w, r, err):
	default:
		done := "changes"
		if approve {
			done = "approve"
		}
		http.Redirect(w, r, "/admin/listings?done="+done, http.StatusSeeOther)
	}
}

func kindLabel(k string) string {
	if k == "agent" {
		return "Agent — owner authority not confirmed"
	}
	return "Owner"
}

func fmtPin(lat, lng *float64) string {
	if lat == nil || lng == nil {
		return "—"
	}
	return strconv.FormatFloat(*lat, 'f', 5, 64) + ", " + strconv.FormatFloat(*lng, 'f', 5, 64)
}
