package mandates

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/pages"
)

// Handler serves the landlord's link, /m/{token}. No account: holding the
// link (sent to the landlord's phone) is the proof.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Offer shows the request.
func (h *Handler) Offer(w http.ResponseWriter, r *http.Request) {
	o, err := h.svc.ByToken(r.Context(), chi.URLParam(r, "token"))
	if h.failed(w, r, err) {
		return
	}
	render.Component(w, r, http.StatusOK, pages.MandateOffer(offerView(r, o, "", "")))
}

// Decide records approve / decline / report / withdraw.
func (h *Handler) Decide(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	d := Decision(r.PostFormValue("decision"))
	o, err := h.svc.Decide(r.Context(), token, d, Visitor{IP: auth.ClientIP(r), UserAgent: r.UserAgent()})
	var verr ValidationError
	if errors.As(err, &verr) {
		o, err = h.svc.ByToken(r.Context(), token)
		if h.failed(w, r, err) {
			return
		}
		render.Component(w, r, http.StatusConflict, pages.MandateOffer(offerView(r, o, "", verr["form"])))
		return
	}
	if h.failed(w, r, err) {
		return
	}
	answered := map[Decision]string{Approve: "approved", Decline: "declined", Report: "reported", Withdraw: "withdrawn"}[d]
	render.Component(w, r, http.StatusOK, pages.MandateOffer(offerView(r, o, answered, "")))
}

func (h *Handler) failed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		render.Error(w, r, http.StatusNotFound)
	default:
		slog.ErrorContext(r.Context(), "mandates", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	}
	return true
}

func offerView(r *http.Request, o *Offer, answered, errMsg string) pages.MandateOfferView {
	v := pages.MandateOfferView{
		Action: r.URL.Path, State: string(o.State), AgentName: o.Agent.Name, Months: o.M.Months,
		IdentityVerified: o.Agent.IdentityVerifiedAt != nil, LicenceVerified: o.Agent.LicenseVerifiedAt != nil,
		PropertyName: o.P.Name, Answered: answered, Error: errMsg,
	}
	if v.AgentName == "" {
		v.AgentName = "An agent"
	}
	if o.Agent.Phone != nil {
		v.AgentTel = phone.Pretty(*o.Agent.Phone)
	}
	if ap := o.Agent.Edges.AgentProfile; ap != nil {
		v.Agency = ap.AgencyName
	}
	if v.PropertyName == "" {
		v.PropertyName = "Your property"
	}
	var area []string
	if n, ok := geo.NeighbourhoodBySlug(o.P.Neighbourhood); ok {
		area = append(area, n.Label)
	}
	if o.P.Landmark != "" {
		area = append(area, o.P.Landmark)
	}
	for i, a := range area {
		if i > 0 {
			v.Area += " · "
		}
		v.Area += a
	}
	if o.M.ValidUntil != nil {
		v.ValidUntil = o.M.ValidUntil.Format("2 January 2006")
	}
	return v
}
