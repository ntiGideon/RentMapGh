package listings

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/partials"
)

// Resumable upload protocol (video-uploader.js):
//
//	POST /listings/{id}/video/uploads          size=N        → 201 {"id": …}
//	POST /listings/{id}/video/uploads/{uid}?offset=K  <bytes> → 200 Upload-Offset: K+n
//	GET  /listings/{id}/video/uploads/{uid}                  → 204 Upload-Offset: K
//
// The last piece answers with the refreshed panel and Upload-Complete: 1.
// A piece that doesn't start at the server's offset gets 409 with the right
// one; a refused video gets 422 with the reason as plain text.

// VideoPanel returns the video section (polled while processing).
func (h *Handler) VideoPanel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	h.videoPanel(w, r, id, http.StatusOK, "")
}

// StartVideoUpload opens a resumable upload.
func (h *Handler) StartVideoUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	size, err := strconv.ParseInt(r.PostFormValue("size"), 10, 64)
	if err != nil {
		size = 0
	}
	uid, err := h.svc.StartVideoUpload(r.Context(), actor(r), id, size)
	if h.videoRefused(w, r, err) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, `{"id":"`+uid.String()+`"}`)
}

// VideoUploadOffset tells a resuming client where to carry on.
func (h *Handler) VideoUploadOffset(w http.ResponseWriter, r *http.Request) {
	id, uid, ok := parseUpload(w, r)
	if !ok {
		return
	}
	have, err := h.svc.VideoUploadOffset(actor(r), id, uid)
	if h.videoRefused(w, r, err) {
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(have, 10))
	w.WriteHeader(http.StatusNoContent)
}

// VideoChunk appends one piece of an upload.
func (h *Handler) VideoChunk(w http.ResponseWriter, r *http.Request) {
	id, uid, ok := parseUpload(w, r)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		http.Error(w, "bad offset", http.StatusBadRequest)
		return
	}
	// 2 MB over a slow 3G link can take a minute.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(3 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(4 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, MaxVideoChunk+1)

	have, done, err := h.svc.AppendVideoChunk(r.Context(), actor(r), id, uid, offset, r.Body)
	w.Header().Set("Upload-Offset", strconv.FormatInt(have, 10))
	var off OffsetError
	switch {
	case errors.As(err, &off):
		w.WriteHeader(http.StatusConflict)
		return
	case err != nil && !done && !isRefusal(err):
		// The connection dropped mid-piece: keep what arrived.
		slog.InfoContext(r.Context(), "listings: video chunk cut short", "upload", uid, "have", have, "err", err)
		w.WriteHeader(http.StatusConflict)
		return
	case h.videoRefused(w, r, err):
		return
	case done:
		w.Header().Set("Upload-Complete", "1")
		h.videoPanel(w, r, id, http.StatusOK, "")
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// UploadVideo is the no-script form: the whole file in one multipart post,
// streamed to disk without buffering it in memory.
func (h *Handler) UploadVideo(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(31 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, MaxVideoBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	err = ValidationError{"video": "Choose a video to upload."}
	for {
		part, perr := mr.NextPart()
		if perr != nil {
			break
		}
		if part.FormName() == "video" && part.FileName() != "" {
			err = h.svc.AddVideoFile(r.Context(), actor(r), id, part)
			_ = part.Close()
			break
		}
		_ = part.Close()
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		err = sizeError(MaxVideoBytes + 1)
	}
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.renderVideoStep(w, r, id, http.StatusUnprocessableEntity, verr["video"])
	case h.notFound(w, r, err):
	default:
		redirect(w, r, editURL(id, "photos"))
	}
}

// DeleteVideo removes the video.
func (h *Handler) DeleteVideo(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	err := h.svc.DeleteVideo(r.Context(), actor(r), id)
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.videoPanel(w, r, id, http.StatusUnprocessableEntity, verr["video"])
	case h.notFound(w, r, err):
	case !htmx.IsPartial(r):
		redirect(w, r, editURL(id, "photos"))
	default:
		h.videoPanel(w, r, id, http.StatusOK, "")
	}
}

func (h *Handler) videoPanel(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int, msg string) {
	d, err := h.svc.Load(r.Context(), actor(r), id)
	if h.notFound(w, r, err) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	render.Component(w, r, status, partials.VideoPanel(videoView(d, h.svc.VideoEnabled(), reqctx.DataSaver(r.Context()), msg)))
}

func (h *Handler) renderVideoStep(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int, msg string) {
	d, err := h.svc.Load(r.Context(), actor(r), id)
	if h.notFound(w, r, err) {
		return
	}
	h.renderStep(w, r, status, d, "photos", nil, ValidationError{"video": msg})
}

func isRefusal(err error) bool {
	var verr ValidationError
	return errors.As(err, &verr) || errors.Is(err, ErrUploadGone) || errors.Is(err, ErrVideoOff) || errors.Is(err, ErrNotFound)
}

// videoRefused answers the script's upload calls with plain-text reasons.
func (h *Handler) videoRefused(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var verr ValidationError
	status, msg := http.StatusInternalServerError, "Something went wrong on our side."
	switch {
	case errors.As(err, &verr):
		status, msg = http.StatusUnprocessableEntity, verr["video"]
	case errors.Is(err, ErrUploadGone):
		status, msg = http.StatusGone, "This upload expired. Please choose the video again."
	case errors.Is(err, ErrVideoOff), errors.Is(err, ErrNotFound):
		status, msg = http.StatusNotFound, "Not found."
	default:
		slog.ErrorContext(r.Context(), "listings: video upload", "err", err)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
	return true
}

func parseUpload(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	id, ok := parseID(w, r)
	if !ok {
		return id, uuid.Nil, false
	}
	uid, err := uuid.Parse(chi.URLParam(r, "uploadID"))
	if err != nil {
		http.NotFound(w, r)
		return id, uuid.Nil, false
	}
	return id, uid, true
}
