package verification

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	ev "rentmapgh/internal/ent/verification"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

// AdminQueue lists verifications by status (?status=pending|approved|rejected).
func (h *Handler) AdminQueue(w http.ResponseWriter, r *http.Request) {
	status := ev.Status(r.URL.Query().Get("status"))
	if ev.StatusValidator(status) != nil || status == ev.StatusWithdrawn {
		status = ev.StatusPending
	}
	items, err := h.svc.Queue(r.Context(), status, 100)
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: queue", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	counts, err := h.svc.Counts(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: counts", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	view := partials.AdminQueue{Status: string(status), Counts: map[string]int{}}
	switch r.URL.Query().Get("done") {
	case "approved":
		view.Notice = "Approved. The user has been notified by SMS."
	case "rejected":
		view.Notice = "Rejected. The user has been told what to fix by SMS."
	}
	for k, n := range counts {
		view.Counts[string(k)] = n
	}
	now := time.Now()
	for _, v := range items {
		row := partials.QueueRow{
			ID: v.ID.String(), Kind: string(v.Kind), Submitted: ago(v.CreatedAt, now),
		}
		if u := v.Edges.User; u != nil {
			row.Name, row.Phone = u.Name, phone.Mask(deref(u.Phone))
			if row.Name == "" {
				row.Name = "No name"
			}
		}
		if v.ReviewedAt != nil {
			row.Decided = ago(*v.ReviewedAt, now)
		}
		view.Rows = append(view.Rows, row)
	}
	render.Page(w, r, http.StatusOK, pages.AdminQueue(view), partials.AdminQueueBody(view))
}

// AdminReview shows one verification with its evidence.
func (h *Handler) AdminReview(w http.ResponseWriter, r *http.Request) {
	h.renderReview(w, r, http.StatusOK, nil)
}

func (h *Handler) renderReview(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	rev, err := h.svc.ForReview(r.Context(), actor(r), id)
	if ent.IsNotFound(err) {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: review", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	view := reviewView(rev, actor(r).UserID == rev.V.UserID)
	view.Errors = errs
	view.PickedReason = r.PostFormValue("reason")
	view.Note = r.PostFormValue("note")
	render.Page(w, r, status, pages.AdminReview(view), partials.AdminReviewBody(view))
}

// AdminFile streams one decrypted evidence photo. Never cached anywhere.
func (h *Handler) AdminFile(w http.ResponseWriter, r *http.Request) {
	vid, err1 := uuid.Parse(chi.URLParam(r, "id"))
	fid, err2 := uuid.Parse(chi.URLParam(r, "fileID"))
	if err1 != nil || err2 != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	f, data, err := h.svc.File(r.Context(), vid, fid)
	if err != nil {
		if !ent.IsNotFound(err) {
			slog.ErrorContext(r.Context(), "admin: evidence file", "err", err)
		}
		render.Error(w, r, http.StatusNotFound)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", f.ContentType)
	hd.Set("Content-Length", strconv.Itoa(len(data)))
	hd.Set("Cache-Control", "private, no-store")
	hd.Set("Content-Disposition", "inline")
	hd.Set("X-Robots-Tag", "noindex")
	hd.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; sandbox")
	_, _ = w.Write(data)
}

// AdminDecide records approve/reject.
func (h *Handler) AdminDecide(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		render.Error(w, r, http.StatusBadRequest)
		return
	}
	d := Decision{Approve: r.PostFormValue("decision") == "approve", Reason: r.PostFormValue("reason"), Note: r.PostFormValue("note")}
	err = h.svc.Decide(r.Context(), actor(r), id, d)
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		h.renderReview(w, r, http.StatusUnprocessableEntity, verr)
	case errors.Is(err, ErrSelfReview), errors.Is(err, ErrNotPending):
		h.renderReview(w, r, http.StatusConflict, map[string]string{"form": err.Error()})
	case ent.IsNotFound(err):
		render.Error(w, r, http.StatusNotFound)
	case err != nil:
		slog.ErrorContext(r.Context(), "admin: decide", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	default:
		done := "approved"
		if !d.Approve {
			done = "rejected"
		}
		htmx.Redirect(w, r, "/admin/verifications?done="+done)
	}
}

func reviewView(rev *Review, own bool) partials.AdminReviewView {
	v, u := rev.V, rev.User
	view := partials.AdminReviewView{
		ID: v.ID.String(), Kind: string(v.Kind), Status: string(v.Status),
		Submitted: v.CreatedAt.Format("2 Jan 2006, 15:04"), CardNumber: rev.CardNumber,
		Last4: v.IDNumberLast4, License: v.LicenseNumber, Own: own,
		Purged: v.EvidencePurgedAt != nil,
	}
	for _, rs := range Reasons[string(v.Kind)] {
		view.Reasons = append(view.Reasons, partials.ReasonOption{Code: rs.Code, Label: rs.Label, Message: rs.Message})
	}
	if u != nil {
		view.Person = partials.AdminPerson{
			Name: u.Name, Phone: phone.Pretty(deref(u.Phone)), Since: u.CreatedAt.Format("Jan 2006"),
			PhoneVerified: u.PhoneVerifiedAt != nil, Active: UserIsActive(u),
		}
		for _, ra := range u.Edges.Roles {
			view.Person.Roles = append(view.Person.Roles, string(ra.Role))
		}
		if p := u.Edges.AgentProfile; p != nil {
			view.Agency = p.AgencyName
		}
	}
	for _, f := range v.Edges.Files {
		view.Files = append(view.Files, partials.EvidenceFile{
			URL:   "/admin/verifications/" + v.ID.String() + "/files/" + f.ID.String(),
			Label: fileLabel(string(f.Kind)),
		})
	}
	if v.ReviewedAt != nil {
		view.Decided = v.ReviewedAt.Format("2 Jan 2006, 15:04")
		if v.Status == ev.StatusRejected {
			view.DecisionMessage = UserMessage(v)
		}
	}
	for _, hv := range rev.History {
		view.History = append(view.History, partials.HistoryItem{
			Kind: string(hv.Kind), Status: string(hv.Status), When: hv.CreatedAt.Format("2 Jan 2006"),
		})
	}
	return view
}

func fileLabel(kind string) string {
	switch kind {
	case "id_front":
		return "Card — front"
	case "id_back":
		return "Card — back"
	case "selfie":
		return "Selfie with card"
	case "license_doc":
		return "Licence certificate"
	}
	return kind
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + " days ago"
	}
}
