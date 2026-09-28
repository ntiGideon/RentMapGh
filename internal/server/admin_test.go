package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/auditevent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/roleassignment"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/modules/admin"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/sms"
)

// staff signs someone in and gives them a staff role (onboarding never offers one).
func staff(t *testing.T, h http.Handler, d *db.DB, capture *sms.Capture, phoneNo, name, role string) (*browser, *ent.User) {
	t.Helper()
	b := signInAs(t, h, d, capture, phoneNo, name, "renter")
	u := d.Ent.User.Query().Where(user.Phone("+233" + strings.TrimPrefix(phoneNo, "0"))).OnlyX(context.Background())
	d.Ent.RoleAssignment.Create().SetUserID(u.ID).SetRole(roleassignment.Role(role)).ExecX(context.Background())
	return b, u
}

func userByPhone(t *testing.T, d *db.DB, phoneNo string) *ent.User {
	return d.Ent.User.Query().Where(user.Phone("+233" + strings.TrimPrefix(phoneNo, "0"))).WithRoles().OnlyX(context.Background())
}

func audited(t *testing.T, d *db.DB, action, target string) bool {
	n, err := d.Ent.AuditEvent.Query().Where(auditevent.Action(action), auditevent.TargetID(target)).Count(context.Background())
	require.NoError(t, err)
	return n > 0
}

func TestAdminUsersAndSuspension(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	ll, ids := liveListingsBy(t, h, d, capture, "0244000071", 1)
	renter := signInAs(t, h, d, capture, "0244000072", "Kofi Mensah", "renter")
	mod, modU := staff(t, h, d, capture, "0244000073", "Mo Moderator", "moderator")
	adm, admU := staff(t, h, d, capture, "0244000074", "Ama Admin", "admin")
	landlord, kofi := userByPhone(t, d, "0244000071"), userByPhone(t, d, "0244000072")

	// Only staff get in; some pages and actions are admins only.
	assert.Equal(t, http.StatusForbidden, renter.do("GET", "/admin/users", nil, false).Code)
	rec := mod.do("GET", "/admin/users?q=0244000072", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Kofi Mensah")
	assert.NotContains(t, rec.Body.String(), "Akua Owusu", "the phone search is exact")
	assert.Contains(t, mod.do("GET", "/admin/users?q=kofi", nil, false).Body.String(), "Kofi Mensah")
	assert.Equal(t, http.StatusForbidden, mod.do("GET", "/admin/audit", nil, false).Code)
	assert.Equal(t, http.StatusForbidden, mod.do("POST", "/admin/users/"+kofi.ID.String()+"/roles", url.Values{"role": {"agent"}, "grant": {"1"}}, false).Code)
	body := mod.do("GET", "/admin/users/"+kofi.ID.String(), nil, false).Body.String()
	assert.Contains(t, body, "Suspend account")
	assert.NotContains(t, body, "View as this user", "moderators can't view as")

	// Suspension needs a reason, never hits staff or yourself.
	lURL := "/admin/users/" + landlord.ID.String()
	rec = mod.do("POST", lURL+"/suspend", url.Values{"note": {"  "}}, false)
	assert.Contains(t, rec.Header().Get("Location"), "error=")
	rec = mod.do("POST", "/admin/users/"+admU.ID.String()+"/suspend", url.Values{"note": {"x"}}, false)
	assert.Contains(t, rec.Header().Get("Location"), "error=")
	rec = mod.do("POST", "/admin/users/"+modU.ID.String()+"/suspend", url.Values{"note": {"x"}}, false)
	assert.Contains(t, rec.Header().Get("Location"), "error=")

	rec = mod.do("POST", lURL+"/suspend", url.Values{"note": {"Asked two renters for deposits on WhatsApp."}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, lURL+"?done=suspended", rec.Header().Get("Location"))
	l := d.Ent.Listing.GetX(ctx, uuidOf(t, ids[0]))
	assert.Equal(t, listing.StatusPaused, l.Status, "live listings are paused")
	assert.Equal(t, http.StatusSeeOther, ll.do("GET", "/listings", nil, false).Code, "signed out everywhere")
	assert.True(t, audited(t, d, "admin.user_suspended", landlord.ID.String()))
	body = mod.do("GET", lURL, nil, false).Body.String()
	assert.Contains(t, body, "Asked two renters for deposits")
	assert.Contains(t, body, "Reactivate")

	rec = mod.do("POST", lURL+"/reactivate", url.Values{}, false)
	assert.Equal(t, lURL+"?done=reactivated", rec.Header().Get("Location"))
	assert.Equal(t, user.StatusActive, d.Ent.User.GetX(ctx, landlord.ID).Status)
	assert.Equal(t, listing.StatusPaused, d.Ent.Listing.GetX(ctx, l.ID).Status, "the lister resumes listings themselves")

	// Roles: admins only, and never their own admin role.
	kURL := "/admin/users/" + kofi.ID.String()
	rec = adm.do("POST", kURL+"/roles", url.Values{"role": {"agent"}, "grant": {"1"}}, false)
	assert.Equal(t, kURL+"?done=role", rec.Header().Get("Location"))
	assert.ElementsMatch(t, []string{"renter", "agent"}, rolesOf(userByPhone(t, d, "0244000072")))
	adm.do("POST", kURL+"/roles", url.Values{"role": {"agent"}, "grant": {"0"}}, false)
	assert.ElementsMatch(t, []string{"renter"}, rolesOf(userByPhone(t, d, "0244000072")))
	rec = adm.do("POST", "/admin/users/"+admU.ID.String()+"/roles", url.Values{"role": {"admin"}, "grant": {"0"}}, false)
	assert.Contains(t, rec.Header().Get("Location"), "error=")
	assert.True(t, audited(t, d, "admin.role_grant", kofi.ID.String()))

	// The audit log shows it all.
	rec = adm.do("GET", "/admin/audit?action=admin.", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "admin.user_suspended")
	assert.Contains(t, rec.Body.String(), "Mo Moderator")

	// Metrics render.
	rec = mod.do("GET", "/admin/metrics", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "North star")
}

func TestAdminViewAs(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	_, ids := liveListingsBy(t, h, d, capture, "0244000081", 1)
	signInAs(t, h, d, capture, "0244000082", "Kofi Mensah", "renter")
	adm, admU := staff(t, h, d, capture, "0244000083", "Ama Admin", "admin")
	mod, modU := staff(t, h, d, capture, "0244000084", "Mo Moderator", "moderator")
	kofi := userByPhone(t, d, "0244000082")
	kURL := "/admin/users/" + kofi.ID.String()

	// Moderators can't; nobody can view as staff.
	assert.Equal(t, http.StatusForbidden, mod.do("POST", kURL+"/view-as", url.Values{}, false).Code)
	rec := adm.do("POST", "/admin/users/"+modU.ID.String()+"/view-as", url.Values{}, false)
	assert.Contains(t, rec.Header().Get("Location"), "view_as_denied")

	rec = adm.do("POST", kURL+"/view-as", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))
	assert.True(t, audited(t, d, "admin.view_as_started", kofi.ID.String()))

	// Seeing the site as Kofi, read-only, with the banner.
	body := adm.do("GET", "/saved", nil, false).Body.String()
	assert.Contains(t, body, "Viewing as Kofi Mensah")
	assert.Equal(t, http.StatusForbidden, adm.do("POST", "/saved/"+ids[0], url.Values{}, false).Code)
	assert.Equal(t, http.StatusForbidden, adm.do("POST", "/l/"+ids[0]+"/message", url.Values{"body": {"hi"}}, false).Code)
	assert.Equal(t, http.StatusForbidden, adm.do("GET", "/admin/users", nil, false).Code, "Kofi isn't staff")
	// It doesn't show up among Kofi's own devices.
	assert.NotContains(t, adm.do("GET", "/account", nil, false).Body.String(), "2 devices")

	// Stop: back to the admin's own session.
	rec = adm.do("POST", "/view-as/stop", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, kURL, rec.Header().Get("Location"))
	rec = adm.do("GET", kURL, nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "Viewing as")
	assert.True(t, audited(t, d, "admin.view_as_stopped", kofi.ID.String()))
	ev := d.Ent.AuditEvent.Query().Where(auditevent.Action("admin.view_as_stopped")).OnlyX(context.Background())
	assert.Equal(t, admU.ID, *ev.ActorID, "recorded against the admin")
}

func TestAdminFlaggedAndTakedown(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	ll, ids := liveListingsBy(t, h, d, capture, "0244000091", 1)
	renter := signInAs(t, h, d, capture, "0244000092", "Kofi Mensah", "renter")
	mod, _ := staff(t, h, d, capture, "0244000093", "Mo Moderator", "moderator")
	id := ids[0]

	// The lister asks for money first: the scam shield flags it.
	rec := renter.do("POST", "/l/"+id+"/message", url.Values{"body": {"Hello, is it available?"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	conv := rec.Header().Get("Location")
	rec = ll.do("POST", conv, url.Values{"body": {"Yes. Send the booking fee by momo first to hold it"}}, false)
	require.Less(t, rec.Code, 400, rec.Body.String())

	rec = mod.do("GET", "/admin/flagged", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Send the booking fee by momo")
	assert.Contains(t, body, "Mobile money")
	assert.NotContains(t, body, "Hello, is it available?", "only flagged messages")
	m := regexpFind(t, body, `/admin/flagged/([0-9a-f-]{36})/reviewed`)
	rec = mod.do("POST", "/admin/flagged/"+m+"/reviewed", url.Values{}, false)
	assert.Equal(t, "/admin/flagged?done=1", rec.Header().Get("Location"))
	assert.NotContains(t, mod.do("GET", "/admin/flagged", nil, false).Body.String(), "booking fee by momo")

	// Takedown: needs a reason, removes the listing, tells the lister.
	rec = mod.do("POST", "/admin/listings/"+id+"/remove", url.Values{"reason": {""}}, false)
	assert.Contains(t, rec.Header().Get("Location"), "remove_error")
	rec = mod.do("POST", "/admin/listings/"+id+"/remove", url.Values{"reason": {"Asking for money before a viewing."}}, false)
	assert.Equal(t, "/admin/listings/"+id+"?done=removed", rec.Header().Get("Location"))
	assert.Equal(t, listing.StatusRemoved, d.Ent.Listing.GetX(ctx, uuidOf(t, id)).Status)
	assert.Contains(t, lastNote(t, d, "+233244000091"), "We took down")
	assert.Equal(t, http.StatusNotFound, renter.do("GET", "/l/"+id, nil, false).Code)
	assert.Contains(t, mod.do("GET", "/admin/listings/"+id, nil, false).Body.String(), "Asking for money before a viewing.")
}

func TestAdminDuplicates(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	// Two landlords, the same pin and the same photos.
	_, a := liveListingsBy(t, h, d, capture, "0244000101", 1)
	_, b := liveListingsBy(t, h, d, capture, "0244000102", 1)
	mod, _ := staff(t, h, d, capture, "0244000103", "Mo Moderator", "moderator")

	svc := admin.NewService(d.Ent, audit.New(d.Ent), nil, nil)
	n, err := svc.ScanDuplicates(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = svc.ScanDuplicates(ctx) // idempotent
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, 1, d.Ent.DuplicateCandidate.Query().CountX(ctx))

	rec := mod.do("GET", "/admin/duplicates", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "same photo")
	assert.Contains(t, body, a[0])
	assert.Contains(t, body, b[0])
	cand := regexpFind(t, body, `/admin/duplicates/([0-9a-f-]{36})`)

	rec = mod.do("POST", "/admin/duplicates/"+cand, url.Values{"decision": {"remove_b"}}, false)
	assert.Equal(t, "/admin/duplicates?done=removed", rec.Header().Get("Location"))
	c := d.Ent.DuplicateCandidate.Query().OnlyX(ctx)
	removed, kept := c.ListingB, c.ListingA
	assert.Equal(t, listing.StatusRemoved, d.Ent.Listing.GetX(ctx, removed).Status)
	assert.Equal(t, listing.StatusActive, d.Ent.Listing.GetX(ctx, kept).Status)
	assert.Equal(t, "actioned", string(c.Status))
	assert.NotContains(t, mod.do("GET", "/admin/duplicates", nil, false).Body.String(), cand)

	n, err = svc.ScanDuplicates(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "a removed listing isn't a candidate any more")
}

func rolesOf(u *ent.User) []string {
	var out []string
	for _, r := range u.Edges.Roles {
		out = append(out, string(r.Role))
	}
	return out
}

func uuidOf(t *testing.T, s string) uuid.UUID {
	id, err := uuid.Parse(s)
	require.NoError(t, err)
	return id
}

func regexpFind(t *testing.T, body, pattern string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(body)
	require.Len(t, m, 2, pattern)
	return m[1]
}
