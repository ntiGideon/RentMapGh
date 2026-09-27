package listings

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/platform/storage"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/partials"
)

// maxFilesPerRequest: the uploader script sends one photo per request; the
// no-JS form may send a handful at once.
const maxFilesPerRequest = 10

// UploadPhotos stores the photos in the "photo" field(s). The uploader
// script gets the refreshed grid (or a plain-text reason); plain form posts
// get the photos step back.
func (h *Handler) UploadPhotos(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	// A photo over a slow 3G link can take a while; the server-wide
	// read timeout is 10 s.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(3 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(4 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, maxFilesPerRequest*MaxPhotoBytes+1<<20)
	if err := r.ParseMultipartForm(4 << 20); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			h.photoError(w, r, id, http.StatusRequestEntityTooLarge, "That's too much at once. Add fewer photos at a time.")
		} else {
			render.Error(w, r, http.StatusBadRequest)
		}
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	files := r.MultipartForm.File["photo"]
	var msg string
	switch {
	case len(files) == 0:
		msg = "Choose a photo to upload."
	case len(files) > maxFilesPerRequest:
		msg = "Add up to " + strconv.Itoa(maxFilesPerRequest) + " photos at a time."
	}
	a := actor(r)
	for _, fh := range files {
		if msg != "" {
			break
		}
		f, err := fh.Open()
		if err != nil {
			render.Error(w, r, http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, MaxPhotoBytes+1))
		_ = f.Close()
		if err != nil {
			render.Error(w, r, http.StatusBadRequest)
			return
		}
		_, err = h.svc.AddPhoto(r.Context(), a, id, data)
		var verr ValidationError
		switch {
		case errors.As(err, &verr):
			msg = verr["photos"]
		case h.notFound(w, r, err):
			return
		}
	}
	if msg != "" {
		h.photoError(w, r, id, http.StatusUnprocessableEntity, msg)
		return
	}
	h.photosDone(w, r, id)
}

// ReorderPhotos saves the order after a drag (form field "order", repeated).
func (h *Handler) ReorderPhotos(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	var order []uuid.UUID
	for _, raw := range r.PostForm["order"] {
		if mid, err := uuid.Parse(raw); err == nil {
			order = append(order, mid)
		}
	}
	h.afterPhotoChange(w, r, id, h.svc.ReorderPhotos(r.Context(), actor(r), id, order))
}

// PhotoAction handles the per-photo buttons: cover, left, right, delete.
func (h *Handler) PhotoAction(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	mid, err := uuid.Parse(chi.URLParam(r, "mediaID"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	a := actor(r)
	switch action := chi.URLParam(r, "action"); action {
	case "delete":
		err = h.svc.DeletePhoto(r.Context(), a, id, mid)
	case "cover", "left", "right":
		err = h.svc.MovePhoto(r.Context(), a, id, mid, action)
	default:
		err = ErrNotFound
	}
	h.afterPhotoChange(w, r, id, err)
}

func (h *Handler) afterPhotoChange(w http.ResponseWriter, r *http.Request, id uuid.UUID, err error) {
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.photoError(w, r, id, http.StatusUnprocessableEntity, verr["photos"])
	case h.notFound(w, r, err):
	default:
		h.photosDone(w, r, id)
	}
}

// photosDone answers a successful change: the grid for the script, the
// photos step for plain forms.
func (h *Handler) photosDone(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if !htmx.IsPartial(r) {
		redirect(w, r, editURL(id, "photos"))
		return
	}
	d, err := h.svc.Load(r.Context(), actor(r), id)
	if h.notFound(w, r, err) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	render.Component(w, r, http.StatusOK, partials.PhotoGrid(photosView(d, "")))
}

// photoError reports a refused change: plain text for the uploader script
// (it shows the reason on that file's tile), the grid with the message for
// htmx buttons, or the whole step for plain forms.
func (h *Handler) photoError(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int, msg string) {
	if r.Header.Get("X-Photo-Upload") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, msg)
		return
	}
	d, err := h.svc.Load(r.Context(), actor(r), id)
	if h.notFound(w, r, err) {
		return
	}
	if htmx.IsPartial(r) {
		render.Component(w, r, http.StatusUnprocessableEntity, partials.PhotoGrid(photosView(d, msg)))
		return
	}
	h.renderStep(w, r, status, d, "photos", nil, ValidationError{"photos": msg})
}

// Media serves a photo rendition, a video poster or a video file. Files
// never change under a URL (new media gets a new ID), so they're cached for
// a year by browsers and the CDN. Videos answer Range requests so players
// can seek.
func (h *Handler) Media(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	file := chi.URLParam(r, "file")
	isVideo := IsVideoFile(file)
	if err != nil || (!IsPhotoFile(file) && !isVideo) {
		http.NotFound(w, r)
		return
	}
	data, err := h.svc.media.Get(r.Context(), MediaKey(id, file))
	if errors.Is(err, storage.ErrNotFound) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "listings: media", "err", err, "id", id)
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	hd := w.Header()
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	hd.Set("X-Content-Type-Options", "nosniff")
	if isVideo {
		// Whole file from the store, then ranges from memory: at ≤1.8 Mbit/s
		// for ≤2 min that's under 30 MB, and the CDN absorbs repeats.
		hd.Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
		return
	}
	hd.Set("Content-Type", "image/jpeg")
	hd.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
