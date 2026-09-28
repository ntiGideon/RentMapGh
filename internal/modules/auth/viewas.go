package auth

import (
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
)

// returnCookie holds the admin's own session token while they view as
// someone else, so stopping (or the view-as session expiring) puts them back.
const returnCookie = "rm_return"

// ViewAs is POST /admin/users/{id}/view-as (admins only): sign this browser
// in as the user for ViewAsTTL, read-only, with a banner to stop.
func (h *Handler) ViewAs(w http.ResponseWriter, r *http.Request) {
	admin := reqctx.CurrentViewer(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || admin.IsViewAs() {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	back := "/admin/users/" + id.String()
	u, err := h.db.User.Query().Where(user.ID(id)).WithRoles().Only(r.Context())
	if ent.IsNotFound(err) {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	if err != nil || u.ID == admin.UserID || u.Status != user.StatusActive || isStaff(u) {
		http.Redirect(w, r, back+"?done=view_as_denied", http.StatusSeeOther)
		return
	}
	own, err := r.Cookie(SessionCookie)
	if err != nil {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	ip, ua := clientIP(r), r.UserAgent()
	token, err := h.sessions.CreateViewAs(r.Context(), u.ID, admin.UserID, ua, ip)
	if err != nil {
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	h.audit.Record(r.Context(), audit.Event{Actor: &admin.UserID, Action: "admin.view_as_started", TargetType: "user",
		TargetID: u.ID.String(), IP: ip, UserAgent: ua})
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: see setSession
		Name: returnCookie, Value: own.Value, Path: "/", MaxAge: int(h.sessions.TTL().Seconds()),
		HttpOnly: true, Secure: h.cookies.secure, SameSite: http.SameSiteLaxMode,
	})
	h.cookies.setSession(w, token, ViewAsTTL)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// StopViewAs is POST /view-as/stop: end the support session and go back
// to the admin's own.
func (h *Handler) StopViewAs(w http.ResponseWriter, r *http.Request) {
	v := reqctx.CurrentViewer(r.Context())
	if !v.IsViewAs() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_ = h.sessions.Revoke(r.Context(), v.UserID, v.SessionID)
	h.audit.Record(r.Context(), audit.Event{Actor: &v.ViewingAs, Action: "admin.view_as_stopped", TargetType: "user",
		TargetID: v.UserID.String(), IP: clientIP(r), UserAgent: r.UserAgent()})
	if !h.restoreOwn(w, r) {
		h.cookies.clearSession(w)
	}
	http.Redirect(w, r, "/admin/users/"+v.UserID.String(), http.StatusSeeOther)
}

// restoreOwn swaps the admin's own session back in from returnCookie.
func (h *Handler) restoreOwn(w http.ResponseWriter, r *http.Request) bool {
	ck, err := r.Cookie(returnCookie)
	if err != nil || ck.Value == "" {
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: returnCookie, Path: "/", MaxAge: -1, //nolint:gosec // G124: see setSession
		HttpOnly: true, Secure: h.cookies.secure, SameSite: http.SameSiteLaxMode})
	if _, err := h.sessions.Resolve(r.Context(), ck.Value); err != nil {
		return false
	}
	h.cookies.setSession(w, ck.Value, h.sessions.TTL())
	return true
}

// ReadOnlyViewAs refuses changes from a view-as session: support can look,
// never act as the user. Stopping and signing out still work.
func ReadOnlyViewAs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := reqctx.CurrentViewer(r.Context()); v.IsViewAs() && r.Method != http.MethodGet && r.Method != http.MethodHead &&
			r.URL.Path != "/view-as/stop" && r.URL.Path != "/logout" {
			render.Forbidden(w, r, "Read-only while viewing as this user", "Stop viewing as them to make changes.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isStaff(u *ent.User) bool {
	return slices.ContainsFunc(u.Edges.Roles, func(r *ent.RoleAssignment) bool {
		return r.Role == "moderator" || r.Role == "admin"
	})
}

func mustCookie(r *http.Request, name string) string {
	if ck, err := r.Cookie(name); err == nil {
		return ck.Value
	}
	return ""
}
