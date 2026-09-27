package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/ent/viewing"
)

func TestNotificationsFeedbackReports(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	ll, ids := liveListingsBy(t, h, d, capture, "0244000101", 1)
	id := ids[0]
	lid := uuid.MustParse(id)
	landlord := d.Ent.User.Query().Where(user.Phone("+233244000101")).OnlyX(ctx)

	// A viewing request lands in the landlord's notification centre.
	renter := signInAs(t, h, d, capture, "0244000102", "Kofi Mensah", "renter")
	rec := renter.do("POST", "/l/"+id+"/viewing", url.Values{"date": {time.Now().Add(48 * time.Hour).Format("2006-01-02")}, "time": {"10:00"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	page := ll.do("GET", "/listings", nil, false).Body.String()
	assert.Regexp(t, `data-badge="notifications" class="[^"]*grid[^"]*">1<`, page, "the bell shows 1")
	rec = ll.do("GET", "/notifications", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Kofi wants to view")
	assert.Regexp(t, `data-badge="notifications" class="[^"]*hidden[^"]*">0<`, ll.do("GET", "/listings", nil, false).Body.String(), "read once seen")

	// Feedback after the viewing.
	v := d.Ent.Viewing.Query().OnlyX(ctx)
	d.Ent.Viewing.UpdateOne(v).SetStatus(viewing.StatusConfirmed).SetStartsAt(time.Now().Add(-3 * time.Hour)).ExecX(ctx)
	fb := "/viewings/" + v.ID.String() + "/feedback"
	assert.Contains(t, renter.do("GET", "/viewings/"+v.ID.String(), nil, false).Body.String(), "How was the viewing?")
	rec = renter.do("GET", fb, nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "didn&#39;t show up")
	rec = renter.do("POST", fb, url.Values{"outcome": {"happened"}, "accuracy": {"as_described"}, "interested": {"yes"}}, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Thanks for the feedback")
	assert.Equal(t, viewing.StatusCompleted, d.Ent.Viewing.GetX(ctx, v.ID).Status)
	assert.Equal(t, http.StatusNotFound, ll.do("GET", fb, nil, false).Code, "renters only")

	// The lister sees the renter's record on the viewing.
	assert.Contains(t, ll.do("GET", "/viewings/"+v.ID.String(), nil, false).Body.String(), "1 of 1 past viewings attended")

	// Earned badges show on the listing page.
	for i := range 3 {
		d.Ent.Viewing.Create().SetListingID(lid).SetRenterID(v.RenterID).SetListerID(landlord.ID).SetStatus(viewing.StatusCompleted).
			SetStartsAt(time.Now().Add(-time.Duration(30+i) * time.Hour)).SetCreatedAt(time.Now().Add(-80 * time.Hour)).
			SetRespondedAt(time.Now().Add(-79 * time.Hour)).SetAccuracy(viewing.AccuracyAsDescribed).ExecX(ctx)
	}
	lp := publicPage(t, newBrowser(t, h), id)
	assert.Contains(t, lp, "Replies within a day")
	assert.Contains(t, lp, "Reliable for viewings")
	assert.Contains(t, lp, "As described")
	assert.Contains(t, lp, "Report listing")

	// Reporting the listing; a moderator sees it.
	rec = renter.do("GET", "/l/"+id+"/report", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	rec = renter.do("POST", "/l/"+id+"/report", url.Values{"reason": {"fake"}, "note": {"Photos are from another house"}}, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Thanks for telling us")
	assert.Equal(t, http.StatusForbidden, ll.do("POST", "/l/"+id+"/report", url.Values{"reason": {"fake"}}, false).Code, "not your own")
	mod := signInAs(t, h, d, capture, "0244000109", "Esi", "renter", "moderator")
	queue := mod.do("GET", "/admin/reports", nil, false).Body.String()
	assert.Contains(t, queue, "Photos or details aren&#39;t real")
	rid := regexp.MustCompile(`/admin/reports/([0-9a-f-]{36})`).FindStringSubmatch(queue)
	require.Len(t, rid, 2)
	review := mod.do("GET", "/admin/reports/"+rid[1], nil, false).Body.String()
	assert.Contains(t, review, "Photos are from another house")
	assert.Contains(t, review, "/admin/listings/"+id)
}
