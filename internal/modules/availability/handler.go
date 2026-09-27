package availability

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

var meta = layouts.Meta{Title: "Is it still available?", NoIndex: true}

// ── The SMS link: /c/{token} ─────────────────────────────────────────────

// LinkPage shows the three answers. Opening the link changes nothing.
func (h *Handler) LinkPage(w http.ResponseWriter, r *http.Request) {
	l, ok := h.fromToken(w, r)
	if !ok {
		return
	}
	h.render(w, r, http.StatusOK, l, "ask", "", r.URL.Path, false)
}

// LinkAnswer applies an answer from the SMS link. The token proves it's the
// lister: it went to their phone.
func (h *Handler) LinkAnswer(w http.ResponseWriter, r *http.Request) {
	l, ok := h.fromToken(w, r)
	if !ok {
		return
	}
	h.answer(w, r, l, r.URL.Path, false)
}

func (h *Handler) fromToken(w http.ResponseWriter, r *http.Request) (*ent.Listing, bool) {
	id, err := h.svc.Open(chi.URLParam(r, "token"))
	if err != nil {
		render.Component(w, r, http.StatusGone, pages.Confirm(meta, pages.ConfirmView{Step: "done",
			Done: "This link has expired. Open Your listings on RentMap to update your place."}))
		return nil, false
	}
	l, err := h.svc.Listing(r.Context(), id)
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return nil, false
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer") // keep the token out of Referer headers
	return l, true
}

// ── Signed in: /listings/{id}/rented ─────────────────────────────────────

// RentedPage is where "Mark rented" in Your listings leads: the question
// first, then the change.
func (h *Handler) RentedPage(w http.ResponseWriter, r *http.Request) {
	l, ok := h.own(w, r)
	if !ok {
		return
	}
	h.render(w, r, http.StatusOK, l, "via", "", r.URL.Path, true)
}

// RentedAnswer marks the listing rented with the answer.
func (h *Handler) RentedAnswer(w http.ResponseWriter, r *http.Request) {
	l, ok := h.own(w, r)
	if !ok {
		return
	}
	h.answer(w, r, l, r.URL.Path, true)
}

func (h *Handler) own(w http.ResponseWriter, r *http.Request) (*ent.Listing, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return nil, false
	}
	l, err := h.svc.Listing(r.Context(), id)
	if err != nil || l.ListerID != reqctx.CurrentViewer(r.Context()).UserID {
		render.Error(w, r, http.StatusNotFound)
		return nil, false
	}
	return l, true
}

// ── Shared ───────────────────────────────────────────────────────────────

func (h *Handler) answer(w http.ResponseWriter, r *http.Request, l *ent.Listing, action string, signedIn bool) {
	ans := Answer(r.PostFormValue("answer"))
	via := r.PostFormValue("via")
	if ans == IsRented && via == "" {
		h.render(w, r, http.StatusOK, l, "via", "", action, signedIn) // ask first, change after
		return
	}
	a := Actor{UserID: l.ListerID, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
	got, err := h.svc.Confirm(r.Context(), a, l.ID, ans, via)
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.render(w, r, http.StatusUnprocessableEntity, l, "ask", verr["form"], action, signedIn)
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "availability: confirm", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	done := map[Answer]string{
		StillAvailable: "Your place is marked available and back at the top of search. We'll check again in a few days.",
		PauseIt:        "Paused. It's hidden from search until you resume it in Your listings.",
		IsRented:       "Congratulations on renting it! It's off the map now, and anyone with a viewing booked has been told.",
	}[ans]
	v := pages.ConfirmView{Step: "done", Done: done, Headline: got.Headline}
	if signedIn {
		v.ListingsURL = "/listings?done=" + string(got.Status)
	}
	render.Component(w, r, http.StatusOK, pages.Confirm(meta, v))
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, l *ent.Listing, step, errMsg, action string, signedIn bool) {
	v := pages.ConfirmView{Action: action, Headline: l.Headline, Step: step, Error: errMsg,
		Status: statusWords(l.Status), Confirmed: confirmedWords(l, h.svc.now())}
	if v.Headline == "" {
		v.Headline = "Your listing"
	}
	if signedIn {
		v.ListingsURL = "/listings"
	}
	render.Component(w, r, status, pages.Confirm(meta, v))
}

func statusWords(s listing.Status) string {
	switch s {
	case listing.StatusActive:
		return "Live on the map"
	case listing.StatusExpired:
		return "Hidden from search (not confirmed recently)"
	case listing.StatusPaused:
		return "Paused"
	case listing.StatusRented:
		return "Marked rented"
	}
	return "Not live"
}

func confirmedWords(l *ent.Listing, now time.Time) string {
	if l.LastConfirmedAt == nil {
		return "Never confirmed."
	}
	days := int(now.Sub(*l.LastConfirmedAt).Hours() / 24)
	switch days {
	case 0:
		return "Last confirmed today."
	case 1:
		return "Last confirmed yesterday."
	}
	return "Last confirmed " + strconv.Itoa(days) + " days ago."
}

// ── Renters: "already rented?" ───────────────────────────────────────────

// ReportRented is a signed-in renter saying the place is taken.
func (h *Handler) ReportRented(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	v := reqctx.CurrentViewer(r.Context())
	err = h.svc.ReportRented(r.Context(), Actor{UserID: v.UserID, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}, id)
	if errors.Is(err, ErrNotFound) {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "availability: report", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	if htmx.IsPartial(r) {
		htmx.Toast(w, "success", "Thanks — we've asked the lister to confirm.")
		render.Component(w, r, http.StatusOK, pages.ReportedRented())
		return
	}
	http.Redirect(w, r, "/l/"+id.String(), http.StatusSeeOther)
}
