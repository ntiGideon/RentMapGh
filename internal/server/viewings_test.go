package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/notification"
	"rentmapgh/internal/ent/user"
)

var (
	slotRe    = regexp.MustCompile(`name="starts_at" value="([0-9T:\-]+Z)"`)
	viewingRe = regexp.MustCompile(`^/viewings/([0-9a-f-]{36})`)
)

func TestViewingFlowOverHTTP(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ll, ids := liveListingsBy(t, h, d, capture, "0244000051", 1)
	id := ids[0]

	// The landlord opens every day, 6:00–20:00.
	form := url.Values{}
	for day := range 7 {
		n := string(rune('0' + day))
		form.Set("day_"+n, "1")
		form.Set("from_"+n, "6:00")
		form.Set("to_"+n, "20:00")
	}
	rec := ll.do("POST", "/viewings/hours", form, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, ll.do("GET", "/viewings", nil, false).Body.String(), "Hours: Sun 6:00–20:00")

	// Booking needs an account: the gate sends strangers to sign in and back.
	anon := newBrowser(t, h)
	rec = anon.do("GET", "/l/"+id+"/viewing", nil, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/login?next=%2Fl%2F"+id+"%2Fviewing")
	page := anon.do("GET", strings.TrimSuffix(anon.do("GET", "/l/"+id, nil, false).Header().Get("Location"), ""), nil, false).Body.String()
	assert.Contains(t, page, "Request a viewing")

	renter := signInAs(t, h, d, capture, "0244000052", "Kofi Mensah", "renter")
	rec = renter.do("GET", "/l/"+id+"/viewing", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	m := slotRe.FindStringSubmatch(rec.Body.String())
	require.Len(t, m, 2, "slots offered")
	rec = renter.do("POST", "/l/"+id+"/viewing", url.Values{"starts_at": {m[1]}, "note": {"Coming with my sister"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	vm := viewingRe.FindStringSubmatch(rec.Header().Get("Location"))
	require.Len(t, vm, 2)
	vURL := "/viewings/" + vm[1]
	assert.Contains(t, lastNote(t, d, "+233244000051"), "Kofi wants to view", "the landlord is notified")

	// Before confirmation: no address, no phone.
	rec = renter.do("GET", vURL, nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Waiting for reply")
	assert.NotContains(t, rec.Body.String(), "Blue gate")
	assert.NotContains(t, rec.Body.String(), "024 400 0051")
	assert.Equal(t, http.StatusNotFound, renter.do("GET", vURL+"/calendar.ics", nil, false).Code)
	assert.Contains(t, renter.do("GET", "/l/"+id, nil, false).Header().Get("Location"), "/l/"+id)

	// Strangers can't see it.
	stranger := signInAs(t, h, d, capture, "0244000053", "Yaw", "renter")
	assert.Equal(t, http.StatusNotFound, stranger.do("GET", vURL, nil, false).Code)

	// The landlord sees it waiting and accepts.
	assert.Contains(t, ll.do("GET", "/viewings", nil, false).Body.String(), "Needs your answer")
	rec = ll.do("POST", vURL+"/accept", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Contains(t, lastNote(t, d, "+233244000052"), "Viewing confirmed")

	// Confirmed: the renter gets the exact place, directions and the phone.
	rec = renter.do("GET", vURL, nil, false)
	body := rec.Body.String()
	assert.Contains(t, body, "Where to go")
	assert.Contains(t, body, "Blue gate", "the landmark unlocks")
	assert.Contains(t, body, "https://www.google.com/maps/dir/?api=1&amp;destination=6.669700,-1.558800")
	assert.Contains(t, body, "024 400 0051")
	rec = renter.do("GET", vURL+"/calendar.ics", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "BEGIN:VEVENT")
	assert.Contains(t, rec.Body.String(), "Blue gate")

	// The landlord sees the renter's phone; the listing page points to the booking.
	assert.Contains(t, ll.do("GET", vURL, nil, false).Body.String(), "024 400 0052")
	loc := renter.do("GET", "/l/"+id, nil, false).Header().Get("Location")
	assert.Contains(t, renter.do("GET", loc, nil, false).Body.String(), "Your viewing ·")

	// The renter cancels; the landlord hears about it.
	rec = renter.do("POST", vURL+"/cancel", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, lastNote(t, d, "+233244000051"), "Viewing cancelled")
	assert.Contains(t, renter.do("GET", vURL, nil, false).Body.String(), "Cancelled")

	// Landlords can't book their own place.
	assert.Equal(t, http.StatusForbidden, ll.do("GET", "/l/"+id+"/viewing", nil, false).Code)
}

// lastNote is the newest notification title for the user with that phone
// (SMS copies depend on quiet hours; the notification always exists).
func lastNote(t *testing.T, d *db.DB, phone string) string {
	t.Helper()
	ctx := context.Background()
	u := d.Ent.User.Query().Where(user.Phone(phone)).OnlyX(ctx)
	n, err := d.Ent.Notification.Query().Where(notification.UserID(u.ID)).Order(ent.Desc(notification.FieldCreatedAt)).First(ctx)
	if err != nil {
		return ""
	}
	return n.Title
}
