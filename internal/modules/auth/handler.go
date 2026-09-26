package auth

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/middleware"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

type Handler struct {
	db       *ent.Client
	otp      *OTP
	sessions *Sessions
	audit    *audit.Log
	cookies  cookies
	devInbox string // dev-only link to the Mailpit inbox shown on the code step
}

type HandlerConfig struct {
	Secret   string
	Secure   bool   // site served over HTTPS
	DevInbox string // "" outside development
}

func NewHandler(db *ent.Client, otp *OTP, sessions *Sessions, log *audit.Log, cfg HandlerConfig) *Handler {
	return &Handler{db: db, otp: otp, sessions: sessions, audit: log,
		cookies: cookies{secret: []byte(cfg.Secret), secure: cfg.Secure}, devInbox: cfg.DevInbox}
}

// Sessions exposes the session store to other modules (account page).
func (h *Handler) Sessions() *Sessions { return h.sessions }

// ClearSessionCookie signs the browser out (after account deletion).
func (h *Handler) ClearSessionCookie(w http.ResponseWriter) { h.cookies.clearSession(w) }

// SetSessionCookie re-issues the cookie after a token rotation.
func (h *Handler) SetSessionCookie(w http.ResponseWriter, token string) {
	h.cookies.setSession(w, token, h.sessions.TTL())
}

// Login shows the phone step. ?change=1 drops a pending number.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	next := SafeNext(r.URL.Query().Get("next"))
	if reqctx.CurrentViewer(r.Context()) != nil {
		http.Redirect(w, r, orDefault(next, "/account"), http.StatusSeeOther) //nolint:gosec // G710: next is validated by SafeNext
		return
	}
	if r.URL.Query().Get("change") == "1" {
		h.cookies.clearPending(w)
	} else if p := h.cookies.pending(r, time.Now()); p != "" {
		// Came back mid-flow (e.g. refreshed): continue at the code step.
		h.renderVerify(w, r, http.StatusOK, partials.VerifyForm{Phone: p, Next: next, DevInbox: h.devInbox})
		return
	}
	h.renderPhone(w, r, http.StatusOK, partials.PhoneForm{Next: next})
}

// SendCode handles the phone step.
func (h *Handler) SendCode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	f := partials.PhoneForm{Phone: r.PostFormValue("phone"), Next: SafeNext(r.PostFormValue("next"))}
	if r.PostFormValue("website") != "" { // honeypot
		slog.InfoContext(r.Context(), "auth: honeypot tripped", "ip", clientIP(r))
		h.renderPhone(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	e164, err := phone.NormalizeGhana(f.Phone)
	if err != nil {
		f.Error = err.Error()
		h.renderPhone(w, r, http.StatusUnprocessableEntity, f)
		return
	}

	issued, err := h.otp.Request(r.Context(), e164, clientIP(r))
	var cd CooldownError
	switch {
	case errors.As(err, &cd):
		// A code is already on its way: go to the code step rather than
		// making the user wait on the phone step.
		issued.ResendAt = time.Now().Add(cd.Wait)
	case isUserFacing(err):
		f.Error = err.Error()
		h.renderPhone(w, r, http.StatusUnprocessableEntity, f)
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "auth: request code", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}

	h.cookies.setPending(w, e164, time.Now())
	if !htmx.IsPartial(r) {
		http.Redirect(w, r, withNext("/login/verify", f.Next), http.StatusSeeOther) //nolint:gosec // G710: next is validated by SafeNext
		return
	}
	w.Header().Set("HX-Push-Url", withNext("/login/verify", f.Next))
	h.renderVerify(w, r, http.StatusOK, partials.VerifyForm{Phone: e164, Next: f.Next, DevInbox: h.devInbox, ResendAt: issued.ResendAt})
}

// VerifyPage shows the code step (direct visits, refreshes, no-JS).
func (h *Handler) VerifyPage(w http.ResponseWriter, r *http.Request) {
	next := SafeNext(r.URL.Query().Get("next"))
	p := h.cookies.pending(r, time.Now())
	if p == "" {
		http.Redirect(w, r, LoginURL(next), http.StatusSeeOther) //nolint:gosec // G710: LoginURL validates next with SafeNext
		return
	}
	h.renderVerify(w, r, http.StatusOK, partials.VerifyForm{Phone: p, Next: next, DevInbox: h.devInbox})
}

// Verify checks the code, signs the user in and redirects.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	next := SafeNext(r.PostFormValue("next"))
	e164 := h.cookies.pending(r, time.Now())
	if e164 == "" {
		htmx.Redirect(w, r, LoginURL(next))
		return
	}
	f := partials.VerifyForm{Phone: e164, Next: next, DevInbox: h.devInbox}

	ip, ua := clientIP(r), r.UserAgent()
	err := h.otp.Verify(r.Context(), e164, r.PostFormValue("code"), ip)
	var wrong WrongCodeError
	switch {
	case errors.As(err, &wrong), isUserFacing(err):
		f.Error = err.Error()
		f.Burnt = errors.Is(err, ErrTooManyAttempts) || errors.Is(err, ErrCodeExpired)
		h.renderVerify(w, r, http.StatusUnprocessableEntity, f)
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "auth: verify", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}

	u, created, err := SignIn(r.Context(), h.db, e164, ip, ua)
	if errors.Is(err, ErrSuspended) {
		h.cookies.clearPending(w)
		render.Forbidden(w, r, "Account suspended", ErrSuspended.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "auth: sign in", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	token, _, err := h.sessions.Create(r.Context(), u.ID, ua, ip)
	if err != nil {
		slog.ErrorContext(r.Context(), "auth: create session", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	h.cookies.clearPending(w)
	h.cookies.setSession(w, token, h.sessions.TTL())
	if u.DataSaver { // the account-level choice follows the user to new devices
		middleware.SetDataSaverCookie(w, true)
	}
	slog.InfoContext(r.Context(), "auth: signed in", "user", u.ID, "new", created)

	dest := orDefault(next, "/account")
	if u.OnboardedAt == nil {
		dest = withNext("/onboarding", next)
	}
	htmx.Redirect(w, r, dest)
}

// Resend issues a fresh code for the pending phone.
func (h *Handler) Resend(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	next := SafeNext(r.PostFormValue("next"))
	e164 := h.cookies.pending(r, time.Now())
	if e164 == "" {
		htmx.Redirect(w, r, LoginURL(next))
		return
	}
	f := partials.VerifyForm{Phone: e164, Next: next, DevInbox: h.devInbox}
	issued, err := h.otp.Request(r.Context(), e164, clientIP(r))
	var cd CooldownError
	switch {
	case errors.As(err, &cd):
		f.ResendAt = time.Now().Add(cd.Wait)
		f.Error = cd.Error()
		h.renderVerify(w, r, http.StatusUnprocessableEntity, f)
	case isUserFacing(err):
		f.Error = err.Error()
		h.renderVerify(w, r, http.StatusUnprocessableEntity, f)
	case err != nil:
		slog.ErrorContext(r.Context(), "auth: resend", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		f.ResendAt = issued.ResendAt
		f.Notice = "We sent a new code. Older codes no longer work."
		h.renderVerify(w, r, http.StatusOK, f)
	}
}

// Logout ends the current session.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if v := reqctx.CurrentViewer(r.Context()); v != nil {
		if err := h.sessions.Revoke(r.Context(), v.UserID, v.SessionID); err != nil && !errors.Is(err, ErrNoSession) {
			slog.ErrorContext(r.Context(), "auth: logout", "err", err)
		}
		h.audit.Record(r.Context(), audit.Event{Actor: &v.UserID, Action: audit.Logout, IP: clientIP(r), UserAgent: r.UserAgent()})
	}
	h.cookies.clearSession(w)
	htmx.Redirect(w, r, "/")
}

func (h *Handler) renderPhone(w http.ResponseWriter, r *http.Request, status int, f partials.PhoneForm) {
	render.Page(w, r, status, pages.Login(partials.LoginPhone(f)), partials.LoginPhone(f))
}

func (h *Handler) renderVerify(w http.ResponseWriter, r *http.Request, status int, f partials.VerifyForm) {
	f.PhoneMasked = phone.Mask(f.Phone)
	render.Page(w, r, status, pages.Login(partials.LoginVerify(f)), partials.LoginVerify(f))
}

func isUserFacing(err error) bool {
	for _, e := range []error{ErrTooManyCodes, ErrDailyCap, ErrSendFailed, ErrCodeMalformed, ErrCodeExpired, ErrTooManyAttempts} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func withNext(path, next string) string {
	if next == "" {
		return path
	}
	return path + "?next=" + url.QueryEscape(next)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// clientIP is RemoteAddr without the port (RealIP middleware has already
// applied trusted proxy headers when TRUST_PROXY is on).
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ClientIP is exported for other modules' audit records.
func ClientIP(r *http.Request) string { return clientIP(r) }
