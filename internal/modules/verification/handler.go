package verification

import (
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"time"

	ev "rentmapgh/internal/ent/verification"
	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

// maxRequest bounds a whole upload request (3 photos + fields).
const maxRequest = 3*MaxFileBytes + 1<<20

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Service exposes the service to other modules (account page, admin).
func (h *Handler) Service() *Service { return h.svc }

func actor(r *http.Request) Actor {
	return Actor{UserID: reqctx.CurrentViewer(r.Context()).UserID, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
}

// IdentityPage shows the Ghana Card form, or the current state if a review
// is open or done.
func (h *Handler) IdentityPage(w http.ResponseWriter, r *http.Request) {
	v := reqctx.CurrentViewer(r.Context())
	st, err := h.svc.StatusFor(r.Context(), v.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "verification: status", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	f := partials.IdentityForm{RetentionDays: h.svc.RetentionDays()}
	if st.Identity != nil {
		switch st.Identity.Status {
		case ev.StatusApproved:
			http.Redirect(w, r, "/account", http.StatusSeeOther)
			return
		case ev.StatusPending:
			f.Pending = true
		case ev.StatusRejected:
			f.Rejected = UserMessage(st.Identity)
		}
	}
	render.Component(w, r, http.StatusOK, pages.Verify("Verify your identity", partials.Identity(f)))
}

func (h *Handler) SubmitIdentity(w http.ResponseWriter, r *http.Request) {
	files, ok := h.parse(w, r, "id_front", "id_back", "selfie")
	if !ok {
		return
	}
	f := partials.IdentityForm{CardNumber: r.PostFormValue("card_number"), Consent: r.PostFormValue("consent") == "1",
		RetentionDays: h.svc.RetentionDays()}
	_, err := h.svc.SubmitIdentity(r.Context(), actor(r), IdentityInput{
		CardNumber: f.CardNumber, Consent: f.Consent,
		Front: files["id_front"], Back: files["id_back"], Selfie: files["selfie"],
	})
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		f.Errors = verr
		f.Reattach = len(files) > 0
		render.Page(w, r, http.StatusUnprocessableEntity, pages.Verify("Verify your identity", partials.Identity(f)), partials.Identity(f))
	case errors.Is(err, ErrPending), errors.Is(err, ErrAlreadyVerified):
		htmx.Redirect(w, r, "/account")
	case err != nil:
		slog.ErrorContext(r.Context(), "verification: submit identity", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		htmx.Redirect(w, r, "/account?submitted=identity")
	}
}

// LicensePage is for agents.
func (h *Handler) LicensePage(w http.ResponseWriter, r *http.Request) {
	v := reqctx.CurrentViewer(r.Context())
	st, err := h.svc.StatusFor(r.Context(), v.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "verification: status", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	f := partials.LicenseForm{}
	if st.License != nil {
		switch st.License.Status {
		case ev.StatusApproved:
			http.Redirect(w, r, "/account", http.StatusSeeOther)
			return
		case ev.StatusPending:
			f.Pending = true
		case ev.StatusRejected:
			f.Rejected = UserMessage(st.License)
			f.Number = st.License.LicenseNumber
		}
	}
	render.Component(w, r, http.StatusOK, pages.Verify("Verify your agent licence", partials.License(f)))
}

func (h *Handler) SubmitLicense(w http.ResponseWriter, r *http.Request) {
	files, ok := h.parse(w, r, "license_doc")
	if !ok {
		return
	}
	f := partials.LicenseForm{Number: r.PostFormValue("license_number"), Agency: r.PostFormValue("agency_name")}
	v := reqctx.CurrentViewer(r.Context())
	_, err := h.svc.SubmitLicense(r.Context(), actor(r), v.Roles, LicenseInput{Number: f.Number, AgencyName: f.Agency, Document: files["license_doc"]})
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		f.Errors = verr
		f.Reattach = len(files) > 0
		render.Page(w, r, http.StatusUnprocessableEntity, pages.Verify("Verify your agent licence", partials.License(f)), partials.License(f))
	case errors.Is(err, ErrPending), errors.Is(err, ErrAlreadyVerified), errors.Is(err, ErrNotAgent):
		htmx.Redirect(w, r, "/account")
	case err != nil:
		slog.ErrorContext(r.Context(), "verification: submit licence", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		htmx.Redirect(w, r, "/account?submitted=license")
	}
}

// parse reads a multipart form and the named file fields (each ≤ MaxFileBytes).
// Missing files are simply absent from the map.
func (h *Handler) parse(w http.ResponseWriter, r *http.Request, names ...string) (map[string][]byte, bool) {
	// Photos over 3G take longer than the server-wide read timeout allows.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(3 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, maxRequest)
	if err := r.ParseMultipartForm(4 << 20); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			render.Error(w, r, http.StatusRequestEntityTooLarge)
		} else {
			render.Error(w, r, http.StatusBadRequest)
		}
		return nil, false
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	out := map[string][]byte{}
	for _, name := range names {
		fh := first(r.MultipartForm.File[name])
		if fh == nil || fh.Size == 0 {
			continue
		}
		if fh.Size > MaxFileBytes {
			render.Error(w, r, http.StatusRequestEntityTooLarge)
			return nil, false
		}
		b, err := readAll(fh)
		if err != nil {
			render.Error(w, r, http.StatusBadRequest)
			return nil, false
		}
		out[name] = b
	}
	return out, true
}

func first(fs []*multipart.FileHeader) *multipart.FileHeader {
	if len(fs) == 0 {
		return nil
	}
	return fs[0]
}

func readAll(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
}
