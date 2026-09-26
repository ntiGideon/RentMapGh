package users

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/platform/storage"
	"rentmapgh/internal/server/middleware"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

const maxAvatarBytes = 12 << 20

func (h *Handler) UploadAvatar(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes+1<<20)
	if err := r.ParseMultipartForm(2 << 20); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			render.Error(w, r, http.StatusRequestEntityTooLarge)
		} else {
			render.Error(w, r, http.StatusBadRequest)
		}
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	file, _, err := r.FormFile("avatar")
	if err != nil {
		h.renderAccount(w, r, http.StatusUnprocessableEntity, map[string]string{"avatar": "Choose a photo to upload."}, "")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes))
	_ = file.Close()
	if err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	err = h.svc.SetAvatar(r.Context(), actor(r), data)
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.renderAccount(w, r, http.StatusUnprocessableEntity, verr, "")
	case err != nil:
		slog.ErrorContext(r.Context(), "users: avatar", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		h.after(w, r, "Your photo was updated.")
	}
}

func (h *Handler) RemoveAvatar(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveAvatar(r.Context(), actor(r)); err != nil {
		slog.ErrorContext(r.Context(), "users: remove avatar", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	h.after(w, r, "Your photo was removed.")
}

// Avatar serves a profile photo. The ?v= hash changes whenever the photo
// does, so matching requests can be cached forever.
func (h *Handler) Avatar(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	key, data, err := h.svc.Avatar(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "users: serve avatar", "err", err)
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "image/jpeg")
	hd.Set("Content-Length", strconv.Itoa(len(data)))
	hd.Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("v") == auth.AvatarVersion(key) {
		hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hd.Set("Cache-Control", "public, max-age=300")
	}
	_, _ = w.Write(data)
}

func (h *Handler) SaveNotifications(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	if err := h.svc.SetNotificationPrefs(r.Context(), actor(r), r.PostForm["on"]); err != nil {
		slog.ErrorContext(r.Context(), "users: prefs", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	h.after(w, r, "Notification preferences saved.")
}

// DataSaver stores the choice on the account and in the cookie the
// DataSaver middleware reads. Full reload, since it changes every page.
func (h *Handler) DataSaver(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	on := r.PostFormValue("on") == "1"
	if err := h.svc.SetDataSaver(r.Context(), reqctx.CurrentViewer(r.Context()).UserID, on); err != nil {
		slog.ErrorContext(r.Context(), "users: data saver", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	middleware.SetDataSaverCookie(w, on)
	http.Redirect(w, r, "/account#data", http.StatusSeeOther)
}

func (h *Handler) SaveLandlordProfile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	h.saved(w, r, h.svc.SaveLandlordProfile(r.Context(), actor(r), LandlordInput{
		DisplayName: r.PostFormValue("display_name"), Bio: r.PostFormValue("bio"),
	}), "Landlord profile saved.")
}

func (h *Handler) SaveAgentProfile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	h.saved(w, r, h.svc.SaveAgentProfile(r.Context(), actor(r), AgentInput{
		AgencyName: r.PostFormValue("agency_name"), Areas: r.PostForm["areas"],
	}), "Agent profile saved.")
}

func (h *Handler) saved(w http.ResponseWriter, r *http.Request, err error, notice string) {
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.renderAccount(w, r, http.StatusUnprocessableEntity, verr, "")
	case err != nil:
		slog.ErrorContext(r.Context(), "users: save", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		h.after(w, r, notice)
	}
}

func (h *Handler) DeletePage(w http.ResponseWriter, r *http.Request) {
	render.Component(w, r, http.StatusOK, pages.AccountDelete(partials.DeleteConfirm("")))
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	if strings.ToUpper(strings.TrimSpace(r.PostFormValue("confirm"))) != "DELETE" {
		render.Component(w, r, http.StatusUnprocessableEntity,
			pages.AccountDelete(partials.DeleteConfirm("Type DELETE in capital letters to confirm.")))
		return
	}
	if err := h.svc.Delete(r.Context(), actor(r)); err != nil {
		slog.ErrorContext(r.Context(), "users: delete account", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	h.auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/account/deleted", http.StatusSeeOther)
}

func (h *Handler) DeletedPage(w http.ResponseWriter, r *http.Request) {
	render.Component(w, r, http.StatusOK, pages.AccountDelete(partials.Deleted()))
}
