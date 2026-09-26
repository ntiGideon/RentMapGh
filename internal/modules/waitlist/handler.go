package waitlist

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	c "rentmapgh/internal/views/components"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

type Handler struct {
	svc     *Service
	baseURL string
	secure  bool // site is served over HTTPS (TLS terminated at the proxy)
}

func NewHandler(svc *Service, baseURL string, secure bool) *Handler {
	return &Handler{svc: svc, baseURL: baseURL, secure: secure}
}

var areaOptions = func() []c.Option {
	opts := make([]c.Option, len(Areas))
	for i, a := range Areas {
		opts[i] = c.Option{Value: a.Slug, Label: a.Label}
	}
	return opts
}()

// flashCookie carries the success state across the no-JS POST→redirect→GET,
// keeping the phone number out of URLs (logs, history, Referer).
const flashCookie = "wl_done"

// Home serves the landing page. ?role= preselects the role (lister CTA).
func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(flashCookie); err == nil {
		http.SetCookie(w, &http.Cookie{Name: flashCookie, Path: "/", MaxAge: -1})
		if v, err := url.ParseQuery(ck.Value); err == nil {
			done := h.done(Result{Phone: v.Get("p"), Already: v.Get("a") == "1"}, v.Get("n"), v.Get("r"))
			render.Component(w, r, http.StatusOK, pages.Home(partials.WaitlistSuccess(done)))
			return
		}
	}
	form := partials.WaitlistForm{Role: r.URL.Query().Get("role"), Areas: areaOptions}
	if form.Role == "" {
		form.Role = "renter"
	}
	render.Component(w, r, http.StatusOK, pages.Home(partials.WaitlistFormCard(form)))
}

func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	in := Input{
		Name:    r.PostFormValue("name"),
		Phone:   r.PostFormValue("phone"),
		Role:    r.PostFormValue("role"),
		Area:    r.PostFormValue("area"),
		Consent: r.PostFormValue("consent") == "1",
		Source:  sourceFrom(r),
	}

	// Honeypot filled → pretend success, store nothing.
	if r.PostFormValue("website") != "" {
		slog.InfoContext(r.Context(), "waitlist: honeypot tripped", "ip", r.RemoteAddr)
		h.success(w, r, Result{Phone: "+233000000000"}, in)
		return
	}

	res, err := h.svc.Join(r.Context(), in)
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		form := partials.WaitlistForm{
			Name: in.Name, Phone: in.Phone, Role: in.Role, Area: in.Area, Consent: in.Consent,
			Areas: areaOptions, Errors: verr,
		}
		render.Page(w, r, http.StatusUnprocessableEntity,
			pages.Home(partials.WaitlistFormCard(form)), partials.WaitlistFormCard(form))
	case err != nil:
		slog.ErrorContext(r.Context(), "waitlist: join", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		slog.InfoContext(r.Context(), "waitlist: joined", "role", in.Role, "area", in.Area, "already", res.Already)
		h.success(w, r, res, in)
	}
}

func (h *Handler) success(w http.ResponseWriter, r *http.Request, res Result, in Input) {
	if htmx.IsPartial(r) {
		render.Component(w, r, http.StatusOK, partials.WaitlistSuccess(h.done(res, in.Name, in.Area)))
		return
	}
	// No-JS: POST → redirect → GET so a refresh doesn't resubmit.
	v := url.Values{"p": {res.Phone}, "n": {in.Name}, "r": {in.Area}}
	if res.Already {
		v.Set("a", "1")
	}
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: v.Encode(), Path: "/", MaxAge: 60,
		HttpOnly: true, Secure: r.TLS != nil || h.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/#join", http.StatusSeeOther)
}

func (h *Handler) done(res Result, name, area string) partials.WaitlistDone {
	return partials.WaitlistDone{
		Name:        name,
		PhoneMasked: phone.Mask(res.Phone),
		AreaLabel:   AreaLabel(area),
		Already:     res.Already,
		ShareURL:    h.baseURL + "/?utm_source=whatsapp_share",
	}
}

// sourceFrom reads utm_source from the page the form was submitted on.
func sourceFrom(r *http.Request) string {
	ref := r.Header.Get("HX-Current-URL")
	if ref == "" {
		ref = r.Referer()
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return u.Query().Get("utm_source")
}
