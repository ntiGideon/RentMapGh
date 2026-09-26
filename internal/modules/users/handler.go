package users

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	ev "rentmapgh/internal/ent/verification"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/modules/verification"
	"rentmapgh/internal/modules/waitlist"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	c "rentmapgh/internal/views/components"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

type Handler struct {
	svc    *Service
	auth   *auth.Handler
	verify *verification.Service
	audit  *audit.Log
}

func NewHandler(svc *Service, a *auth.Handler, verify *verification.Service, log *audit.Log) *Handler {
	return &Handler{svc: svc, auth: a, verify: verify, audit: log}
}

func actor(r *http.Request) Actor {
	return Actor{UserID: reqctx.CurrentViewer(r.Context()).UserID, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
}

// OnboardingPage asks a new user how they'll use RentMap.
func (h *Handler) OnboardingPage(w http.ResponseWriter, r *http.Request) {
	v := reqctx.CurrentViewer(r.Context())
	next := auth.SafeNext(r.URL.Query().Get("next"))
	if v.Onboarded {
		http.Redirect(w, r, orDefault(next, "/account"), http.StatusSeeOther) //nolint:gosec // G710: next is validated by auth.SafeNext
		return
	}
	f := partials.OnboardingForm{Name: v.Name, Roles: v.Roles, Next: next}
	if len(f.Roles) == 0 {
		f.Roles = []string{"renter"}
	}
	render.Component(w, r, http.StatusOK, pages.Onboarding(partials.Onboarding(f)))
}

func (h *Handler) Onboard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	f := partials.OnboardingForm{Name: r.PostFormValue("name"), Roles: r.PostForm["roles"], Next: auth.SafeNext(r.PostFormValue("next"))}
	err := h.svc.Onboard(r.Context(), actor(r), f.Name, f.Roles)
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		f.Errors = verr
		render.Page(w, r, http.StatusUnprocessableEntity, pages.Onboarding(partials.Onboarding(f)), partials.Onboarding(f))
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "users: onboard", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	h.rotate(w, r)
	htmx.Redirect(w, r, orDefault(f.Next, "/account"))
}

// Account shows profile, verification, roles, settings and devices.
func (h *Handler) Account(w http.ResponseWriter, r *http.Request) {
	notice := ""
	switch r.URL.Query().Get("submitted") {
	case "identity":
		notice = "Thanks — your ID is with our team. We'll text you when it's checked, usually within one working day."
	case "license":
		notice = "Thanks — we're checking your licence and will text you the result."
	}
	h.renderAccount(w, r, http.StatusOK, nil, notice)
}

func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	err := h.svc.UpdateName(r.Context(), actor(r), r.PostFormValue("name"))
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.renderAccount(w, r, http.StatusUnprocessableEntity, verr, "")
	case err != nil:
		slog.ErrorContext(r.Context(), "users: update profile", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		h.after(w, r, "Your name was updated.")
	}
}

func (h *Handler) AddRole(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	role := r.PostFormValue("role")
	err := h.svc.AddRole(r.Context(), actor(r), role)
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.renderAccount(w, r, http.StatusUnprocessableEntity, verr, "")
	case err != nil:
		slog.ErrorContext(r.Context(), "users: add role", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		h.rotate(w, r)
		h.after(w, r, partials.RoleLabel(role)+" role added.")
	}
}

func (h *Handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	v := reqctx.CurrentViewer(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || id == v.SessionID {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	err = h.auth.Sessions().Revoke(r.Context(), v.UserID, id)
	switch {
	case errors.Is(err, auth.ErrNoSession):
		render.Error(w, r, http.StatusNotFound)
	case err != nil:
		slog.ErrorContext(r.Context(), "users: revoke session", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		h.audit.Record(r.Context(), audit.Event{Actor: &v.UserID, Action: audit.SessionRevoked, TargetType: "session", TargetID: id.String(),
			IP: auth.ClientIP(r), UserAgent: r.UserAgent()})
		h.after(w, r, "That device has been logged out.")
	}
}

func (h *Handler) RevokeOthers(w http.ResponseWriter, r *http.Request) {
	v := reqctx.CurrentViewer(r.Context())
	n, err := h.auth.Sessions().RevokeOthers(r.Context(), v.UserID, v.SessionID)
	if err != nil {
		slog.ErrorContext(r.Context(), "users: revoke others", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	h.audit.Record(r.Context(), audit.Event{Actor: &v.UserID, Action: audit.SessionsRevokedAll, IP: auth.ClientIP(r), UserAgent: r.UserAgent(),
		Meta: map[string]any{"count": n}})
	h.after(w, r, "All other devices have been logged out.")
}

// after re-renders the account for htmx, or redirects (PRG) without JS.
func (h *Handler) after(w http.ResponseWriter, r *http.Request, notice string) {
	if !htmx.IsPartial(r) {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	h.renderAccount(w, r, http.StatusOK, nil, notice)
}

// rotate swaps the session token after a privilege change.
func (h *Handler) rotate(w http.ResponseWriter, r *http.Request) {
	token, err := h.auth.Sessions().Rotate(r.Context(), reqctx.CurrentViewer(r.Context()).SessionID)
	if err != nil {
		slog.ErrorContext(r.Context(), "users: rotate session", "err", err)
		return
	}
	h.auth.SetSessionCookie(w, token)
}

func (h *Handler) renderAccount(w http.ResponseWriter, r *http.Request, status int, errs map[string]string, notice string) {
	v := reqctx.CurrentViewer(r.Context())
	u, err := h.svc.Get(r.Context(), v.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "users: load account", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	sessions, err := h.auth.Sessions().Active(r.Context(), v.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "users: load sessions", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	st, err := h.verify.StatusFor(r.Context(), v.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "users: load verification", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	fresh := auth.ViewerFor(u, v.SessionID)
	view := partials.AccountView{
		Name:          u.Name,
		Initial:       fresh.Initial(),
		AvatarURL:     fresh.AvatarURL,
		PhonePretty:   phone.Pretty(fresh.Phone),
		PhoneVerified: u.PhoneVerifiedAt != nil,
		MemberSince:   u.CreatedAt.Format("January 2006"),
		Roles:         fresh.Roles,
		Errors:        errs,
		Notice:        notice,
		IsLandlord:    fresh.Has("landlord"),
		IsAgent:       fresh.Has("agent"),
		AreaOptions:   areaOptions,
		Channels:      Channels,
		Prefs:         Prefs(u),
		DataSaver:     u.DataSaver,
	}
	for _, t := range Topics {
		view.Topics = append(view.Topics, partials.Topic{Key: t.Key, Label: t.Label, Hint: t.Hint})
	}
	if p := u.Edges.LandlordProfile; p != nil {
		view.LandlordName, view.LandlordBio = p.DisplayName, p.Bio
	}
	if p := u.Edges.AgentProfile; p != nil {
		view.AgentAgency, view.AgentAreas = p.AgencyName, p.ServiceAreas
	}
	view.Verifications = append(view.Verifications, verifItem(st.Identity, "Identity", "Ghana Card and a selfie, checked by our team.", "/verify/identity", "Verify your identity"))
	if fresh.Has("agent") {
		view.Verifications = append(view.Verifications, verifItem(st.License, "Agent licence", "Real Estate Agency Council licence.", "/verify/licence", "Add your licence"))
	}
	now := time.Now()
	for _, s := range sessions {
		label, mobile := deviceLabel(s.UserAgent)
		view.Devices = append(view.Devices, partials.Device{
			ID: s.ID.String(), Label: label, Mobile: mobile, IP: s.IP,
			LastActive: ago(s.LastSeenAt, now), Current: s.ID == v.SessionID,
		})
	}
	greeting := "Akwaaba, " + fresh.DisplayName()
	render.Page(w, r, status, pages.Account(greeting, partials.Account(view)), partials.Account(view))
}

var areaOptions = func() []c.Option {
	opts := make([]c.Option, 0, len(waitlist.Areas))
	for _, a := range waitlist.Areas {
		opts = append(opts, c.Option{Value: a.Slug, Label: a.Label})
	}
	return opts
}()

// verifItem turns the latest verification of a kind into an account row.
func verifItem(v *ent.Verification, title, body, url, cta string) partials.VerifItem {
	it := partials.VerifItem{Title: title, Body: body, State: "none", ActionURL: url, ActionLbl: cta}
	if v == nil {
		return it
	}
	switch v.Status {
	case ev.StatusPending:
		it.State, it.ActionURL = "pending", ""
		it.When = "Submitted " + v.CreatedAt.Format("2 Jan") + ". We'll text you the result."
	case ev.StatusApproved:
		it.State, it.ActionURL = "approved", ""
		if v.ReviewedAt != nil {
			it.When = "Verified on " + v.ReviewedAt.Format("2 January 2006")
		}
	case ev.StatusRejected:
		it.State, it.Message, it.ActionLbl = "rejected", verification.UserMessage(v), "Try again"
	}
	return it
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
