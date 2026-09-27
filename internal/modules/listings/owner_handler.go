package listings

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/modules/mandates"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/views/partials"
)

// Owner authority: an agent asks the landlord to confirm (mandates
// package). The card lives on the review step; its forms come back here.

func mandateActor(r *http.Request) mandates.Actor {
	a := actor(r)
	return mandates.Actor{UserID: a.UserID, IP: a.IP, UserAgent: a.UserAgent}
}

// OwnerAsk texts the landlord a request.
func (h *Handler) OwnerAsk(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	months, _ := strconv.Atoi(r.PostFormValue("months"))
	_, err := h.mandates.Ask(r.Context(), mandateActor(r), id, mandates.Request{
		LandlordName: r.PostFormValue("landlord_name"), LandlordPhone: r.PostFormValue("landlord_phone"), Months: months})
	h.afterOwner(w, r, id, err)
}

// OwnerResend texts the pending request again.
func (h *Handler) OwnerResend(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	h.afterOwner(w, r, id, h.mandates.Resend(r.Context(), mandateActor(r), id))
}

// OwnerCancel withdraws the pending request.
func (h *Handler) OwnerCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	h.afterOwner(w, r, id, h.mandates.Cancel(r.Context(), mandateActor(r), id))
}

func (h *Handler) afterOwner(w http.ResponseWriter, r *http.Request, id uuid.UUID, err error) {
	var verr mandates.ValidationError
	switch {
	case err == nil:
		redirect(w, r, editURL(id, "review")+"#owner-authority")
	case errors.As(err, &verr):
		d, lerr := h.svc.Load(r.Context(), actor(r), id)
		if h.notFound(w, r, lerr) {
			return
		}
		errs := ValidationError{}
		for k, msg := range verr {
			if k == "form" {
				k = "owner_form"
			}
			errs[k] = msg
		}
		h.renderStep(w, r, http.StatusUnprocessableEntity, d, "review", r.PostForm, errs)
	case errors.Is(err, mandates.ErrNotFound), errors.Is(err, mandates.ErrNoAgent):
		h.notFound(w, r, ErrNotFound)
	default:
		h.notFound(w, r, err)
	}
}

// mandateView is the review step's "Owner authority" card, for agent
// listings only (nil otherwise).
func (h *Handler) mandateView(ctx context.Context, d *Item, form map[string][]string, errs ValidationError) (*partials.MandateView, error) {
	if h.mandates == nil || d.L.ListerKind != listing.ListerKindAgent || d.P == nil {
		return nil, nil
	}
	m, err := h.mandates.Latest(ctx, d.L.ListerID, d.P.ID)
	if err != nil {
		return nil, err
	}
	now := h.mandates.Now()
	v := &partials.MandateView{ListingID: d.L.ID.String(), State: string(mandates.StateOf(m, now)),
		Months: mandates.MonthOptions, FormMonths: 12, Errors: map[string]string{}}
	for k, dst := range map[string]string{"owner_form": "form", "landlord_name": "landlord_name", "landlord_phone": "landlord_phone"} {
		if msg := errs[k]; msg != "" {
			v.Errors[dst] = msg
		}
	}
	if form != nil {
		get := func(k string) string {
			if vs := form[k]; len(vs) > 0 {
				return vs[0]
			}
			return ""
		}
		v.FormName, v.FormPhone = get("landlord_name"), get("landlord_phone")
		if n, err := strconv.Atoi(get("months")); err == nil {
			v.FormMonths = n
		}
	}
	if m != nil {
		fillMandate(v, m, now)
	}
	return v, nil
}

func fillMandate(v *partials.MandateView, m *ent.AgentMandate, now time.Time) {
	v.LandlordName, v.LandlordTel = m.LandlordName, phone.Mask(m.LandlordPhone)
	v.Sent = ago(m.SentAt, now)
	v.CanResend = m.Sends < mandates.MaxSends && now.Sub(m.SentAt) >= mandates.ResendCooldown
	if m.ValidUntil != nil {
		v.ValidUntil = m.ValidUntil.Format("2 January 2006")
	}
}
