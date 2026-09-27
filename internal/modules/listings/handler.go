package listings

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent/unit"
	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/modules/mandates"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

type Handler struct {
	svc         *Service
	mandates    *mandates.Service // owner authority for agent listings (nil: off)
	baseURL     string            // for absolute links (share, canonical, og:image)
	saved       savedCodec
	openViewing func(ctx context.Context, renter, listing uuid.UUID) (url, when string)
}

// HandlerConfig is what the public pages need from the app config.
type HandlerConfig struct {
	BaseURL string
	Secret  string // signs the saved-places cookie
	Secure  bool   // HTTPS: cookies get Secure
	// OpenViewing finds the renter's pending or confirmed viewing of a
	// listing ("" if none), for the listing page's call to action.
	OpenViewing func(ctx context.Context, renter, listing uuid.UUID) (url, when string)
}

func NewHandler(svc *Service, m *mandates.Service, cfg HandlerConfig) *Handler {
	return &Handler{svc: svc, mandates: m, baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		saved: savedCodec{secret: []byte(cfg.Secret), secure: cfg.Secure}, openViewing: cfg.OpenViewing}
}

// Service exposes the service (admin wiring).
func (h *Handler) Service() *Service { return h.svc }

func actor(r *http.Request) Actor {
	v := reqctx.CurrentViewer(r.Context())
	return Actor{UserID: v.UserID, Roles: v.Roles, IdentityVerified: v.IdentityVerified, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
}

// Mine shows "Your listings".
func (h *Handler) Mine(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	items, err := h.svc.Mine(r.Context(), a)
	if err != nil {
		slog.ErrorContext(r.Context(), "listings: mine", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	v := partials.MyListingsView{IdentityVerified: a.IdentityVerified, Counts: map[string]int{}}
	now := time.Now()
	var props []uuid.UUID
	for _, d := range items {
		if d.L.ListerKind == "agent" && d.P != nil {
			props = append(props, d.P.ID)
		}
	}
	var states map[uuid.UUID]mandates.State
	if h.mandates != nil {
		if states, err = h.mandates.States(r.Context(), a.UserID, props); err != nil {
			slog.ErrorContext(r.Context(), "listings: owner authority", "err", err)
		}
	}
	for _, d := range items {
		cv := cardView(d, now)
		if d.L.ListerKind == "agent" && d.P != nil {
			cv.IsAgent, cv.Authority = true, string(states[d.P.ID])
		}
		v.Items = append(v.Items, cv)
		v.Counts[string(d.L.Status)]++
	}
	switch r.URL.Query().Get("done") {
	case "active":
		v.Notice = "Your listing is live. We'll ask you to confirm it's still available every few days."
	case "pending_review":
		v.Notice = "Thanks — our team will check your listing, usually within a day. We'll text you when it's live."
	case "paused":
		v.Notice = "Listing paused. It's hidden from search until you resume it."
	case "rented":
		v.Notice = "Marked as rented. Congratulations!"
	case "confirmed":
		v.Notice = "Thanks — marked available. It's back at the top of search."
	}
	render.Page(w, r, http.StatusOK, pages.MyListings(v), partials.MyListings(v))
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	l, err := h.svc.CreateDraft(r.Context(), actor(r))
	if errors.Is(err, ErrNotLister) {
		render.Forbidden(w, r, "Listing is for landlords and agents", ErrNotLister.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "listings: create", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/listings/"+l.ID.String()+"/edit/location", http.StatusSeeOther)
}

func (h *Handler) AddUnit(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	l, err := h.svc.AddUnit(r.Context(), actor(r), id)
	if h.notFound(w, r, err) {
		return
	}
	http.Redirect(w, r, "/listings/"+l.ID.String()+"/edit/unit", http.StatusSeeOther)
}

// Edit shows one wizard step.
func (h *Handler) Edit(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	step := chi.URLParam(r, "step")
	if stepIndex(step) < 0 {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	a := actor(r)
	d, err := h.svc.Load(r.Context(), a, id)
	if h.notFound(w, r, err) {
		return
	}
	if stepIndex(step) > stepIndex(d.L.WizardStep) { // no skipping ahead
		redirect(w, r, editURL(id, d.L.WizardStep))
		return
	}
	h.renderStep(w, r, http.StatusOK, d, step, nil, nil)
}

// Save handles "Continue": strict validation, then the next step.
func (h *Handler) Save(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	step := chi.URLParam(r, "step")
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	d, errs, err := h.svc.SaveStep(r.Context(), actor(r), id, step, r.PostForm, true)
	if h.notFound(w, r, err) {
		return
	}
	if errs != nil {
		h.renderStep(w, r, http.StatusUnprocessableEntity, d, step, r.PostForm, errs)
		return
	}
	next := NextStep(step)
	if next == "" {
		next = "review"
	}
	redirect(w, r, editURL(id, next))
}

// Autosave stores whatever is valid while the lister types. It answers with
// the save indicator, plus the live move-in preview on the pricing step.
func (h *Handler) Autosave(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	step := chi.URLParam(r, "step")
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	d, _, err := h.svc.SaveStep(r.Context(), actor(r), id, step, r.PostForm, false)
	if errors.Is(err, ErrNotFound) {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "listings: autosave", "err", err)
		render.Component(w, r, http.StatusOK, partials.SaveStatus("", true))
		return
	}
	stamp := time.Now().Format("15:04")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = partials.SaveStatus(stamp, false).Render(r.Context(), w)
	switch {
	case step == "pricing":
		_ = partials.MoveInPreview(moveInView(d.Terms()), true).Render(r.Context(), w)
	case step == "location" && r.PostFormValue("neighbourhood") == "" && d.P.Neighbourhood != "":
		_ = partials.NeighbourhoodField(d.P.Neighbourhood, neighbourhoodOpts, "", true).Render(r.Context(), w)
	}
}

func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	st, err := h.svc.Submit(r.Context(), actor(r), id)
	switch {
	case errors.Is(err, ErrIncomplete), errors.Is(err, ErrTransition):
		redirect(w, r, editURL(id, "review"))
	case h.notFound(w, r, err):
	default:
		htmx.Redirect(w, r, "/listings?done="+string(st))
	}
}

// Action applies pause / resume / mark_rented / relist / withdraw.
func (h *Handler) Action(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if Event(chi.URLParam(r, "event")) == EvMarkRented {
		// Ask "did you find your tenant through RentMap?" first (availability).
		redirect(w, r, "/listings/"+id.String()+"/rented")
		return
	}
	ev := Event(chi.URLParam(r, "event"))
	st, err := h.svc.Act(r.Context(), actor(r), id, ev)
	switch {
	case errors.Is(err, ErrTransition):
		http.Redirect(w, r, "/listings", http.StatusSeeOther)
	case h.notFound(w, r, err):
	case ev == EvConfirm:
		redirect(w, r, "/listings?done=confirmed")
	default:
		redirect(w, r, doneURL(st))
	}
}

func (h *Handler) renderStep(w http.ResponseWriter, r *http.Request, status int, d *Item, step string, form map[string][]string, errs ValidationError) {
	units, err := h.svc.db.Unit.Query().Where(unit.PropertyID(d.P.ID)).Count(r.Context())
	if err != nil {
		units = 1
	}
	v := buildView(d, step, actor(r).IdentityVerified, form, errs, units)
	v.Video = videoView(d, h.svc.VideoEnabled(), reqctx.DataSaver(r.Context()), errs["video"])
	if step == "review" {
		mv, err := h.mandateView(r.Context(), d, form, errs)
		if err != nil {
			slog.ErrorContext(r.Context(), "listings: owner authority", "err", err)
		}
		v.Mandate = mv
	}
	body := map[string]func(partials.WizardView) templ.Component{
		"location": partials.StepLocation, "property": partials.StepProperty, "unit": partials.StepUnit,
		"amenities": partials.StepAmenities, "photos": partials.StepPhotos, "pricing": partials.StepPricing, "details": partials.StepDetails,
		"review": partials.StepReview,
	}[step]
	render.Component(w, r, status, pages.ListingWizard(v, body(v)))
}

// notFound renders 404 for ErrNotFound and 500 for other errors. It reports
// whether it wrote a response.
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		render.Error(w, r, http.StatusNotFound)
	default:
		slog.ErrorContext(r.Context(), "listings", "err", err, "path", r.URL.Path)
		render.Error(w, r, http.StatusInternalServerError)
	}
	return true
}

// editURL is a wizard URL. Steps outside the fixed list fall back to review,
// so the path can only ever be one of ours.
func editURL(id uuid.UUID, step string) string {
	if stepIndex(step) < 0 {
		step = "review"
	}
	return "/listings/" + id.String() + "/edit/" + step
}

// doneURL is "Your listings" with a status notice (a Status constant).
func doneURL(st Status) string { return "/listings?done=" + url.QueryEscape(string(st)) }

// redirect is a 303 to a path built by editURL/doneURL — never user input.
func redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther) //nolint:gosec // G710: path is built from a parsed UUID and fixed step/status names
}

func parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return uuid.Nil, false
	}
	return id, true
}
