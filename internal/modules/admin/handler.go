package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/modules/listings"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/pages"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func actor(r *http.Request) Actor {
	v := reqctx.CurrentViewer(r.Context())
	return Actor{UserID: v.UserID, Roles: v.Roles, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
}

func parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return uuid.Nil, false
	}
	return id, true
}

// fail handles the errors every action shares. A Problem goes back to
// the page it came from as ?error=….
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error, back string) bool {
	var p Problem
	switch {
	case err == nil:
		return false
	case errors.As(err, &p):
		http.Redirect(w, r, back+"?error="+url.QueryEscape(string(p)), http.StatusSeeOther)
	case errors.Is(err, ErrNotFound):
		render.Error(w, r, http.StatusNotFound)
	case errors.Is(err, ErrForbidden):
		render.Forbidden(w, r, "Admins only", "Ask an admin to make this change.")
	default:
		slog.ErrorContext(r.Context(), "admin", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	}
	return true
}

// ── Users ────────────────────────────────────────────────────────────────

func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	f := UserFilter{Q: q.Get("q"), Role: q.Get("role"), Status: q.Get("status"), Page: max(page, 0)}
	us, total, err := h.svc.Users(r.Context(), f)
	if h.fail(w, r, err, "/admin/users") {
		return
	}
	now := h.svc.now()
	v := pages.AdminUsersView{Q: f.Q, Role: f.Role, Status: f.Status, Roles: Roles, Total: total}
	for _, u := range us {
		row := pages.AdminUserRow{URL: "/admin/users/" + u.ID.String(), Name: nameOf(u), Phone: phoneOf(u), Status: string(u.Status),
			Joined: u.CreatedAt.Format("2 Jan 2006"), Seen: "—"}
		var roles []string
		for _, r := range roleNames(u) {
			roles = append(roles, pages.RoleLabel(r))
		}
		row.Roles = strings.Join(roles, ", ")
		if u.LastSeenAt != nil {
			row.Seen = ago(*u.LastSeenAt, now)
		}
		v.Rows = append(v.Rows, row)
	}
	pageURL := func(p int) string {
		q.Set("page", strconv.Itoa(p))
		return "/admin/users?" + q.Encode()
	}
	if f.Page > 0 {
		v.Prev = pageURL(f.Page - 1)
	}
	if (f.Page+1)*PageSize < total {
		v.Next = pageURL(f.Page + 1)
	}
	render.Component(w, r, http.StatusOK, pages.AdminUsers(v))
}

var userNotices = map[string]string{
	"suspended":      "Suspended. They've been signed out and their live listings are paused.",
	"reactivated":    "Reactivated. They can sign in again; paused listings stay paused until they resume them.",
	"role":           "Roles updated. It takes effect on their next page load.",
	"view_as_denied": "You can only view as an active, non-staff account other than your own.",
}

func (h *Handler) User(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	d, err := h.svc.User(r.Context(), id)
	if h.fail(w, r, err, "/admin/users") {
		return
	}
	a := actor(r)
	u := d.U
	now := h.svc.now()
	staff := isStaff(d.Roles)
	v := pages.AdminUserView{
		ID: u.ID.String(), Name: nameOf(u), Phone: phoneOf(u), Status: string(u.Status), Joined: u.CreatedAt.Format("2 Jan 2006"), Seen: "never",
		SuspensionNote: u.SuspensionNote, Sessions: d.Sessions, Viewings: d.Viewings, NoShows: d.NoShows, Filed: d.ReportsFiled,
		CanManageRoles: a.IsAdmin(), CanSuspend: u.ID != a.UserID && !staff,
		CanViewAs: a.IsAdmin() && u.ID != a.UserID && !staff && u.Status == "active",
		Notice:    userNotices[r.URL.Query().Get("done")], Error: r.URL.Query().Get("error"),
	}
	if u.LastSeenAt != nil {
		v.Seen = ago(*u.LastSeenAt, now)
	}
	if u.SuspendedAt != nil {
		v.SuspendedAt = u.SuspendedAt.Format("2 Jan 2006")
	}
	if u.IdentityVerifiedAt != nil {
		v.Verified = append(v.Verified, "ID checked")
	}
	if u.LicenseVerifiedAt != nil {
		v.Verified = append(v.Verified, "Licence checked")
	}
	for _, role := range Roles {
		v.Roles = append(v.Roles, pages.RoleToggle{Role: role, Has: slices.Contains(d.Roles, role)})
	}
	for _, l := range d.Listings {
		v.Listings = append(v.Listings, pages.AdminLink{Label: orDash(l.Headline), Meta: "created " + l.CreatedAt.Format("2 Jan 2006"),
			Status: string(l.Status), URL: "/admin/listings/" + l.ID.String()})
	}
	for _, rp := range d.Reports {
		v.Reports = append(v.Reports, pages.AdminLink{Label: rp.Reason, Meta: string(rp.TargetType) + " · " + ago(rp.CreatedAt, now),
			Status: string(rp.Status), URL: "/admin/reports/" + rp.ID.String()})
	}
	v.Events = h.auditRows(r, d.Events)
	render.Component(w, r, http.StatusOK, pages.AdminUser(v))
}

func (h *Handler) Suspend(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	back := "/admin/users/" + id.String()
	if h.fail(w, r, h.svc.Suspend(r.Context(), actor(r), id, r.PostFormValue("note")), back) {
		return
	}
	http.Redirect(w, r, back+"?done=suspended", http.StatusSeeOther)
}

func (h *Handler) Reactivate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	back := "/admin/users/" + id.String()
	if h.fail(w, r, h.svc.Reactivate(r.Context(), actor(r), id), back) {
		return
	}
	http.Redirect(w, r, back+"?done=reactivated", http.StatusSeeOther)
}

func (h *Handler) SetRole(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	back := "/admin/users/" + id.String()
	err := h.svc.SetRole(r.Context(), actor(r), id, r.PostFormValue("role"), r.PostFormValue("grant") == "1")
	if h.fail(w, r, err, back) {
		return
	}
	http.Redirect(w, r, back+"?done=role", http.StatusSeeOther)
}

// ── Audit log ────────────────────────────────────────────────────────────

func (h *Handler) Audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := AuditFilter{Action: q.Get("action"), Actor: q.Get("actor"), Target: q.Get("target")}
	if t, err := time.Parse(time.RFC3339Nano, q.Get("before")); err == nil {
		f.Before = t
	}
	evs, err := h.svc.Audit(r.Context(), f)
	if h.fail(w, r, err, "/admin/audit") {
		return
	}
	v := pages.AdminAuditView{Action: f.Action, Actor: f.Actor, Target: f.Target, Rows: h.auditRows(r, evs)}
	if len(evs) == PageSize {
		q.Set("before", evs[len(evs)-1].CreatedAt.Format(time.RFC3339Nano))
		v.Next = "/admin/audit?" + q.Encode()
	}
	render.Component(w, r, http.StatusOK, pages.AdminAudit(v))
}

func (h *Handler) auditRows(r *http.Request, evs []*ent.AuditEvent) []pages.AuditRow {
	var ids []uuid.UUID
	for _, e := range evs {
		if e.ActorID != nil {
			ids = append(ids, *e.ActorID)
		}
	}
	names := h.svc.Names(r.Context(), ids)
	out := make([]pages.AuditRow, 0, len(evs))
	for _, e := range evs {
		row := pages.AuditRow{When: e.CreatedAt.Format("2 Jan 15:04"), Action: e.Action, Actor: "system", IP: e.IP,
			Target: e.TargetType, TargetURL: targetURL(e.TargetType, e.TargetID)}
		if e.TargetID != "" {
			row.Target = e.TargetType + " " + shortID(e.TargetID)
		}
		if e.ActorID != nil {
			row.ActorURL = "/admin/users/" + e.ActorID.String()
			row.Actor = shortID(e.ActorID.String())
			if u := names[*e.ActorID]; u != nil {
				row.Actor = nameOf(u)
			}
		}
		if len(e.Meta) > 0 {
			if b, err := json.Marshal(e.Meta); err == nil {
				row.Meta = string(b)
			}
		}
		out = append(out, row)
	}
	return out
}

func targetURL(kind, id string) string {
	if id == "" {
		return ""
	}
	switch kind {
	case "user":
		return "/admin/users/" + id
	case "listing":
		return "/admin/listings/" + id
	case "report":
		return "/admin/reports/" + id
	case "verification":
		return "/admin/verifications/" + id
	}
	return ""
}

// ── Metrics ──────────────────────────────────────────────────────────────

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	m := h.svc.Metrics(r.Context())
	n := strconv.Itoa
	share := "—"
	if m.Rented30 > 0 {
		share = fmt.Sprintf("%d%%", m.RentedVia30*100/m.Rented30)
	}
	v := pages.AdminMetricsView{
		North: []pages.MetricTile{
			{Label: "Last 30 days", Value: n(m.RentedVia30), Note: "rented and found their tenant here"},
			{Label: "Share of rentals", Value: share, Note: n(m.Rented30) + " marked rented in 30 days"},
			{Label: "All time", Value: n(m.RentedViaAll)},
		},
		Groups: []pages.MetricGroup{
			{Title: "People", Tiles: []pages.MetricTile{
				{Label: "Active accounts", Value: n(m.Users), Note: "+" + n(m.Users7) + " this week"},
				{Label: "Renters", Value: n(m.Renters)}, {Label: "Landlords", Value: n(m.Landlords)}, {Label: "Agents", Value: n(m.Agents)},
			}},
			{Title: "Supply", Tiles: []pages.MetricTile{
				{Label: "Live listings", Value: n(m.Live)}, {Label: "Waiting for review", Value: n(m.Pending)},
				{Label: "Published", Value: n(m.Listed30), Note: "last 30 days"},
			}},
			{Title: "Demand (last 30 days)", Tiles: []pages.MetricTile{
				{Label: "Viewing requests", Value: n(m.Viewings30)}, {Label: "Viewings held", Value: n(m.Held30)},
				{Label: "No-shows", Value: n(m.NoShows30)}, {Label: "New conversations", Value: n(m.Conversations30)},
			}},
		},
	}
	for i := len(m.Weeks) - 1; i >= 0; i-- { // newest first
		wk := m.Weeks[i]
		v.Weeks = append(v.Weeks, pages.WeekRow{Label: wk.Start.Format("2 Jan"), Users: wk.Users, Listings: wk.Listings,
			Viewings: wk.Viewings, RentedVia: wk.RentedVia})
	}
	render.Component(w, r, http.StatusOK, pages.AdminMetrics(v))
}

// ── Flagged messages ─────────────────────────────────────────────────────

func (h *Handler) Flagged(w http.ResponseWriter, r *http.Request) {
	ms, err := h.svc.Flagged(r.Context())
	if h.fail(w, r, err, "/admin") {
		return
	}
	var ids []uuid.UUID
	for _, m := range ms {
		ids = append(ids, m.SenderID)
	}
	names := h.svc.Names(r.Context(), ids)
	now := h.svc.now()
	v := pages.AdminFlaggedView{}
	if r.URL.Query().Get("done") == "1" {
		v.Notice = "Cleared from the queue."
	}
	for _, m := range ms {
		row := pages.FlaggedRow{ID: m.ID.String(), Body: m.Body, Flags: strings.Join(flagLabels(m.Flags), " · "),
			SenderURL: "/admin/users/" + m.SenderID.String(), Sender: "someone", When: ago(m.CreatedAt, now)}
		if u := names[m.SenderID]; u != nil {
			row.Sender = nameOf(u)
		}
		if c, err := h.svc.db.Conversation.Get(r.Context(), m.ConversationID); err == nil {
			row.ListingURL = "/admin/listings/" + c.ListingID.String()
			row.Listing = "a listing"
			if l, err := h.svc.db.Listing.Get(r.Context(), c.ListingID); err == nil && l.Headline != "" {
				row.Listing = l.Headline
			}
		}
		v.Rows = append(v.Rows, row)
	}
	render.Component(w, r, http.StatusOK, pages.AdminFlagged(v))
}

// flagLabels names the scam-shield flags (messages/scam.go).
func flagLabels(fs []string) []string {
	labels := map[string]string{"momo": "Mobile money", "pay_first": "Payment before viewing", "booking_fee": "Booking fee",
		"off_platform": "Move off RentMap", "pressure": "Pressure"}
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		if l, ok := labels[f]; ok {
			out = append(out, l)
		} else {
			out = append(out, f)
		}
	}
	return out
}

func (h *Handler) ReviewFlag(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if h.fail(w, r, h.svc.ReviewFlag(r.Context(), actor(r), id), "/admin/flagged") {
		return
	}
	http.Redirect(w, r, "/admin/flagged?done=1", http.StatusSeeOther)
}

// ── Listings ─────────────────────────────────────────────────────────────

func (h *Handler) RemoveListing(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	back := "/admin/listings/" + id.String()
	err := h.svc.RemoveListing(r.Context(), actor(r), id, r.PostFormValue("reason"))
	var p Problem
	if errors.As(err, &p) {
		http.Redirect(w, r, back+"?done=remove_error&msg="+url.QueryEscape(string(p)), http.StatusSeeOther)
		return
	}
	if h.fail(w, r, err, back) {
		return
	}
	http.Redirect(w, r, back+"?done=removed", http.StatusSeeOther)
}

// ── Duplicates ───────────────────────────────────────────────────────────

func (h *Handler) Duplicates(w http.ResponseWriter, r *http.Request) {
	ps, err := h.svc.Duplicates(r.Context())
	if h.fail(w, r, err, "/admin") {
		return
	}
	now := h.svc.now()
	v := pages.AdminDuplicatesView{Error: r.URL.Query().Get("error")}
	switch r.URL.Query().Get("done") {
	case "dismiss":
		v.Notice = "Marked as different places."
	case "removed":
		v.Notice = "Taken down. The lister has been told why."
	}
	side := func(key string, d *listings.Item, lister *ent.User) pages.DupSide {
		s := pages.DupSide{Key: key, Title: listings.Title(d), URL: "/admin/listings/" + d.L.ID.String(), Status: string(d.L.Status),
			Lister: nameOf(lister), ListerURL: "/admin/users/" + lister.ID.String(), Rent: "no rent yet", Listed: ago(d.L.CreatedAt, now)}
		if d.T != nil && d.T.Rent != nil {
			s.Rent = d.T.Rent.String()
		}
		if c := d.Cover(); c != nil {
			s.Photo = listings.MediaURL(c.ID, "w320.jpg")
		}
		return s
	}
	for _, p := range ps {
		row := pages.DupRow{ID: p.C.ID.String(), Score: strconv.Itoa(p.C.Score), Distance: strconv.Itoa(p.C.DistanceM) + " m",
			Photo: "no photo match", Text: fmt.Sprintf("headlines %d%% alike", int(p.C.TextSimilarity*100)),
			A: side("a", p.A, p.LA), B: side("b", p.B, p.LB)}
		if p.C.PhotoBits != nil {
			row.Photo = fmt.Sprintf("photos %d bits apart", *p.C.PhotoBits)
			if *p.C.PhotoBits <= 4 {
				row.Photo = "same photo"
			}
		}
		v.Rows = append(v.Rows, row)
	}
	render.Component(w, r, http.StatusOK, pages.AdminDuplicates(v))
}

func (h *Handler) DecideDuplicate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	decision := r.PostFormValue("decision")
	if h.fail(w, r, h.svc.DecideDuplicate(r.Context(), actor(r), id, decision, ""), "/admin/duplicates") {
		return
	}
	done := "removed"
	if decision == "dismiss" {
		done = "dismiss"
	}
	http.Redirect(w, r, "/admin/duplicates?done="+done, http.StatusSeeOther)
}

// ── helpers ──────────────────────────────────────────────────────────────

func nameOf(u *ent.User) string {
	if u.Name != "" {
		return u.Name
	}
	return "No name yet"
}

// phoneOf shows staff the full number: support has to be able to call.
func phoneOf(u *ent.User) string {
	if u.Phone == nil {
		return "deleted"
	}
	return phone.Pretty(*u.Phone)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
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
	case d < 30*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + " d ago"
	}
	return t.Format("2 Jan 2006")
}
