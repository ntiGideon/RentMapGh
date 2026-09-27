package messages

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/conversation"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

type Handler struct {
	svc      *Service
	photoURL func(uuid.UUID) string
	done     <-chan struct{} // closed on shutdown: streams end
}

// NewHandler: photoURL gives a listing's cover thumbnail; done ends open
// event streams when the server shuts down.
func NewHandler(svc *Service, photoURL func(uuid.UUID) string, done <-chan struct{}) *Handler {
	return &Handler{svc: svc, photoURL: photoURL, done: done}
}

func actor(r *http.Request) Actor {
	v := reqctx.CurrentViewer(r.Context())
	return Actor{UserID: v.UserID, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		render.Error(w, r, http.StatusNotFound)
	case errors.Is(err, ErrForbidden):
		render.Forbidden(w, r, "This is your own listing", "Renters message you from your listing page.")
	default:
		slog.ErrorContext(r.Context(), "messages", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	}
	return true
}

// ── First message ────────────────────────────────────────────────────────

// NewPage is /l/{id}/message: write the first message (or jump to the
// conversation you already have).
func (h *Handler) NewPage(w http.ResponseWriter, r *http.Request) {
	h.renderNew(w, r, http.StatusOK, "", nil)
}

func (h *Handler) renderNew(w http.ResponseWriter, r *http.Request, status int, body string, errs ValidationError) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	a := actor(r)
	l, err := h.svc.db.Listing.Get(r.Context(), id)
	if ent.IsNotFound(err) || (err == nil && l.Status != listing.StatusActive) {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	if h.fail(w, r, err) {
		return
	}
	if l.ListerID == a.UserID {
		h.fail(w, r, ErrForbidden)
		return
	}
	if c, err := h.svc.db.Conversation.Query().Where(conversation.ListingID(id), conversation.RenterID(a.UserID)).Only(r.Context()); err == nil && c.LastMessageAt != nil {
		http.Redirect(w, r, "/messages/"+c.ID.String(), http.StatusSeeOther)
		return
	}
	lister, err := h.svc.db.User.Get(r.Context(), l.ListerID)
	if h.fail(w, r, err) {
		return
	}
	v := pages.NewMessageView{ListingID: id.String(), Headline: orText(l.Headline, "A place on RentMap"), Cover: h.photoURL(id),
		To: orText(lister.Name, "the lister"), Body: body, Errors: errs,
		Suggestions: []string{"Hi, is this place still available?", "When can I come and see it?", "Is the price negotiable?"}}
	render.Component(w, r, status, pages.NewMessage(layouts.Meta{Title: "Message the lister", NoIndex: true, Modules: []string{"js/messages.js"}}, v))
}

// Start sends the first message and opens the conversation.
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	a := actor(r)
	body := r.PostFormValue("body")
	c, err := h.svc.Start(r.Context(), a, id)
	var verr ValidationError
	if errors.As(err, &verr) {
		h.renderNew(w, r, http.StatusUnprocessableEntity, body, verr)
		return
	}
	if h.fail(w, r, err) {
		return
	}
	t, err := h.svc.Load(r.Context(), a, c.ID)
	if h.fail(w, r, err) {
		return
	}
	if _, err := h.svc.Send(r.Context(), a, t, body); errors.As(err, &verr) {
		h.renderNew(w, r, http.StatusUnprocessableEntity, body, verr)
		return
	} else if h.fail(w, r, err) {
		return
	}
	http.Redirect(w, r, "/messages/"+c.ID.String(), http.StatusSeeOther)
}

// ── Threads ──────────────────────────────────────────────────────────────

// Thread is /messages/{id}.
func (h *Handler) Thread(w http.ResponseWriter, r *http.Request) {
	h.renderThread(w, r, http.StatusOK, "", nil)
}

func (h *Handler) renderThread(w http.ResponseWriter, r *http.Request, status int, draft string, errs ValidationError) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	a := actor(r)
	t, err := h.svc.Load(r.Context(), a, id)
	if h.fail(w, r, err) {
		return
	}
	ms, err := h.svc.Messages(r.Context(), t, uuid.Nil)
	if h.fail(w, r, err) {
		return
	}
	h.svc.Read(r.Context(), t)
	v := pages.ThreadView{
		ID: t.C.ID.String(), Them: orText(t.Them.Name, "RentMap user"), ThemRole: themRole(t), Role: t.Role,
		Headline: orText(t.Listing.Headline, "A place on RentMap"), ListingURL: "/l/" + t.Listing.ID.String(),
		Cover: h.photoURL(t.Listing.ID), Bubbles: h.bubbles(t, ms), Draft: draft, Errors: errs, Unlocked: t.Unlocked,
		Reasons: reasonOpts(),
	}
	if t.Role == "renter" && t.Listing.Status == listing.StatusActive {
		v.ViewingURL = "/l/" + t.Listing.ID.String() + "/viewing"
	}
	if n := len(ms); n > 0 {
		v.LastID = ms[n-1].ID.String()
	}
	w.Header().Set("Cache-Control", "private, no-store")
	render.Component(w, r, status, pages.Thread(layouts.Meta{Title: "Messages · " + v.Them, NoIndex: true, Modules: []string{"js/messages.js"}}, v))
}

// Send posts a message. The script gets the new bubbles (everything after
// its last one); the plain form goes back to the thread.
func (h *Handler) Send(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	a := actor(r)
	t, err := h.svc.Load(r.Context(), a, id)
	if h.fail(w, r, err) {
		return
	}
	_, err = h.svc.Send(r.Context(), a, t, r.PostFormValue("body"))
	var verr ValidationError
	if errors.As(err, &verr) {
		if htmx.IsPartial(r) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(verr["body"]))
			return
		}
		h.renderThread(w, r, http.StatusUnprocessableEntity, r.PostFormValue("body"), verr)
		return
	}
	if h.fail(w, r, err) {
		return
	}
	if !htmx.IsPartial(r) {
		http.Redirect(w, r, "/messages/"+id.String(), http.StatusSeeOther)
		return
	}
	h.writeSince(w, r, t)
}

// Since returns the bubbles after ?after= (the stream says "new message";
// the page asks for them) and marks the thread read.
func (h *Handler) Since(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, err := h.svc.Load(r.Context(), actor(r), id)
	if h.fail(w, r, err) {
		return
	}
	h.svc.Read(r.Context(), t)
	h.writeSince(w, r, t)
}

func (h *Handler) writeSince(w http.ResponseWriter, r *http.Request, t *Thread) {
	after, _ := uuid.Parse(r.URL.Query().Get("after"))
	if after == uuid.Nil {
		after, _ = uuid.Parse(r.PostFormValue("after"))
	}
	ms, err := h.svc.Messages(r.Context(), t, after)
	if h.fail(w, r, err) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	render.Component(w, r, http.StatusOK, pages.Bubbles(h.bubbles(t, ms), reasonOpts(), t.C.ID.String()))
}

func (h *Handler) bubbles(t *Thread, ms []*ent.Message) []pages.Bubble {
	var out []pages.Bubble
	theirRead := t.TheirReadAt()
	lastMine := -1
	for i, m := range ms {
		mine := m.SenderID == t.Me.ID
		b := pages.Bubble{ID: m.ID.String(), Mine: mine, Body: m.Body, Time: m.CreatedAt.In(time.FixedZone("GMT", 0)).Format("Mon 15:04")}
		if !mine {
			if !t.Unlocked && HasPhone(m.Body) {
				b.Body, b.PhoneHidden = MaskPhones(m.Body), true
			}
			b.Warnings = Warnings(m.Flags)
			b.CanReport = true
		} else {
			lastMine = i
		}
		out = append(out, b)
	}
	if lastMine >= 0 && theirRead != nil && !theirRead.Before(ms[lastMine].CreatedAt) {
		out[lastMine].Seen = true
	}
	return out
}

// Inbox is /messages.
func (h *Handler) Inbox(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	items, err := h.svc.Inbox(r.Context(), a.UserID)
	if h.fail(w, r, err) {
		return
	}
	var v pages.InboxView
	for _, it := range items {
		row := pages.InboxRow{URL: "/messages/" + it.C.ID.String(), Name: orText(it.Them.Name, "RentMap user"),
			Headline: orText(it.Listing.Headline, "A place on RentMap"), Unread: it.Unread, Cover: h.photoURL(it.Listing.ID)}
		if it.Last != nil {
			masked := it.Last.SenderID != a.UserID && HasPhone(it.Last.Body)
			row.Snippet = snippet(it.Last.Body, masked, 90)
			if it.Last.SenderID == a.UserID {
				row.Snippet = "You: " + row.Snippet
			}
			row.When = ago(it.Last.CreatedAt, h.svc.now())
		}
		v.Rows = append(v.Rows, row)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	render.Component(w, r, http.StatusOK, pages.Inbox(layouts.Meta{Title: "Messages", NoIndex: true}, v))
}

// Report files a report about one message.
func (h *Handler) Report(w http.ResponseWriter, r *http.Request) {
	id, err1 := uuid.Parse(chi.URLParam(r, "id"))
	mid, err2 := uuid.Parse(chi.URLParam(r, "msgID"))
	if err1 != nil || err2 != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	a := actor(r)
	t, err := h.svc.Load(r.Context(), a, id)
	if h.fail(w, r, err) {
		return
	}
	err = h.svc.ReportMessage(r.Context(), a, t, mid, r.PostFormValue("reason"), r.PostFormValue("note"))
	var verr ValidationError
	if !errors.As(err, &verr) && h.fail(w, r, err) {
		return
	}
	if htmx.IsPartial(r) {
		htmx.Toast(w, "success", "Thanks — our team will look at this message.")
		render.Component(w, r, http.StatusOK, templ.Raw(`<p class="text-xs text-fg-muted">Reported. Thank you.</p>`))
		return
	}
	http.Redirect(w, r, "/messages/"+id.String(), http.StatusSeeOther)
}

// ── Events (SSE) ─────────────────────────────────────────────────────────

// Events streams the signed-in user's events: "message" (with the unread
// count) and "read". Pages fetch the details over plain HTTP.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // long-lived: no server write timeout
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no") // proxies: don't buffer
	w.WriteHeader(http.StatusOK)

	events, cancel := h.svc.Hub().Subscribe(a.UserID)
	defer cancel()
	// Start with the current unread count, so a reconnect catches up.
	if n, err := h.svc.Unread(r.Context(), a.UserID); err == nil {
		writeEvent(w, Event{Kind: "unread", Unread: n})
	}
	_ = rc.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	limit := time.NewTimer(10 * time.Minute) // clients reconnect; keeps stale streams bounded
	defer limit.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.done:
			return
		case <-limit.C:
			return
		case e := <-events:
			writeEvent(w, e)
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, e Event) {
	b, _ := json.Marshal(e)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Kind, b)
}

// ── Admin: reports ───────────────────────────────────────────────────────

// AdminReports lists open reports.
func (h *Handler) AdminReports(w http.ResponseWriter, r *http.Request) {
	rs, err := h.svc.OpenReports(r.Context())
	if h.fail(w, r, err) {
		return
	}
	var v pages.AdminReportsView
	now := h.svc.now()
	for _, rp := range rs {
		row := pages.AdminReportRow{URL: "/admin/reports/" + rp.ID.String(), Reason: reasonLabel(rp.Reason), Kind: string(rp.TargetType), When: ago(rp.CreatedAt, now)}
		if rp.SubjectID != nil {
			if u, err := h.svc.db.User.Get(r.Context(), *rp.SubjectID); err == nil {
				row.Subject = orText(u.Name, "RentMap user")
			}
		}
		v.Rows = append(v.Rows, row)
	}
	if r.URL.Query().Get("done") == "1" {
		v.Notice = "Report closed."
	}
	render.Component(w, r, http.StatusOK, pages.AdminReports(v))
}

// AdminReport shows one report with the conversation around it.
func (h *Handler) AdminReport(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	rc, err := h.svc.ForReview(r.Context(), id)
	if h.fail(w, r, err) {
		return
	}
	v := pages.AdminReportView{ID: id.String(), Reason: reasonLabel(rc.R.Reason), Note: rc.R.Note, Status: string(rc.R.Status),
		Reporter: userName(rc.Reporter), Subject: userName(rc.Subject)}
	if rc.Listing != nil {
		v.Listing, v.ListingURL = orText(rc.Listing.Headline, "Listing"), "/admin/listings/"+rc.Listing.ID.String()
	}
	for _, m := range rc.Thread {
		who := "Renter"
		if m.SenderID == rc.Conv.ListerID {
			who = "Lister"
		}
		v.Thread = append(v.Thread, pages.AdminMessage{Who: who, Body: m.Body, Time: m.CreatedAt.Format("2 Jan 15:04"),
			Reported: m.ID == rc.Message.ID, Flags: strings.Join(m.Flags, ", ")})
	}
	render.Component(w, r, http.StatusOK, pages.AdminReport(v))
}

// AdminDecide closes a report.
func (h *Handler) AdminDecide(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	if err := h.svc.Resolve(r.Context(), actor(r), id, r.PostFormValue("decision") == "actioned"); h.fail(w, r, err) {
		return
	}
	http.Redirect(w, r, "/admin/reports?done=1", http.StatusSeeOther)
}

// ── Helpers ──────────────────────────────────────────────────────────────

func themRole(t *Thread) string {
	if t.Role == "lister" {
		return "Renter"
	}
	if t.Listing.ListerKind == listing.ListerKindAgent {
		return "Agent"
	}
	return "Owner"
}

func reasonOpts() []pages.Opt {
	var out []pages.Opt
	for _, r := range ReportReasons {
		out = append(out, pages.Opt{Value: r.Key, Label: r.Label})
	}
	return out
}

func reasonLabel(k string) string {
	for _, r := range ReportReasons {
		if r.Key == k {
			return r.Label
		}
	}
	return k
}

func orText(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func userName(u *ent.User) string {
	if u == nil {
		return "—"
	}
	return orText(u.Name, "RentMap user")
}

func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return t.Format("Mon")
	}
	return t.Format("2 Jan")
}
