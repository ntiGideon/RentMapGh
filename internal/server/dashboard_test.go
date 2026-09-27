package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListerDashboard(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ll, ids := liveListingsBy(t, h, d, capture, "0244000061", 1)
	id := ids[0]
	page := ll.do("GET", "/l/"+id, nil, false).Header().Get("Location")
	require.NotEmpty(t, page)

	// Views: once per visitor per day; bots and the lister don't count.
	anon := newBrowser(t, h)
	for range 3 {
		require.Equal(t, http.StatusOK, anon.do("GET", page, nil, false).Code)
	}
	do(h, "GET", page, nil, map[string]string{"User-Agent": "WhatsApp/2.23 A"})
	do(h, "GET", page, nil, map[string]string{"User-Agent": "Googlebot/2.1"})
	ll.do("GET", page, nil, false)
	renter := signInAs(t, h, d, capture, "0244000062", "Kofi Mensah", "renter")
	renter.do("GET", page, nil, false)

	// A save and a viewing request (a contact).
	require.Less(t, renter.do("POST", "/saved/"+id, url.Values{}, false).Code, 400)
	form := url.Values{}
	for day := range 7 {
		n := string(rune('0' + day))
		form.Set("day_"+n, "1")
		form.Set("from_"+n, "6:00")
		form.Set("to_"+n, "20:00")
	}
	require.Equal(t, http.StatusSeeOther, ll.do("POST", "/viewings/hours", form, false).Code)
	m := slotRe.FindStringSubmatch(renter.do("GET", "/l/"+id+"/viewing", nil, false).Body.String())
	require.Len(t, m, 2)
	rec := renter.do("POST", "/l/"+id+"/viewing", url.Values{"starts_at": {m[1]}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	body := ll.do("GET", "/listings", nil, false).Body.String()
	assert.Contains(t, body, "Last 7 days: 2 views · 1 saves · 1 contacts")
	assert.Contains(t, body, "/listings/leads")
	assert.Contains(t, body, "/viewings/calendar")

	// Stats: a column chart with a tooltip per day and a table view.
	rec = ll.do("GET", "/listings/"+id+"/stats", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	assert.Contains(t, body, "<svg")
	assert.Contains(t, body, "· 2 views")
	assert.Contains(t, body, "Show as a table")
	assert.Contains(t, body, "50.0%", "1 contact per 2 views")
	assert.Equal(t, http.StatusForbidden, renter.do("GET", "/listings/"+id+"/stats", nil, false).Code)
	other := signInAs(t, h, d, capture, "0244000063", "Ama", "landlord")
	assert.Equal(t, http.StatusNotFound, other.do("GET", "/listings/"+id+"/stats", nil, false).Code)
	assert.NotContains(t, other.do("GET", "/listings/leads", nil, false).Body.String(), "Kofi Mensah")

	// Leads: the renter is at "Viewing requested".
	rec = ll.do("GET", "/listings/leads", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	i := strings.Index(body, `aria-label="Viewing requested"`)
	require.Positive(t, i)
	assert.Contains(t, body[i:i+1500], "Kofi Mensah")
	assert.Equal(t, http.StatusForbidden, renter.do("GET", "/listings/leads", nil, false).Code)

	// Calendar: the request sits in its week.
	starts, err := time.Parse(time.RFC3339, m[1])
	require.NoError(t, err)
	rec = ll.do("GET", "/viewings/calendar?week="+starts.Format(time.DateOnly), nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Kofi")
	assert.Contains(t, rec.Body.String(), `data-top="`)
	assert.Contains(t, ll.do("GET", "/viewings/calendar?week=2020-01-01", nil, false).Body.String(), "No viewings this week.")

	// Price edit from the dashboard.
	rec = ll.do("POST", "/listings/"+id+"/price", url.Values{"rent": {"1,500"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/listings?done=price", rec.Header().Get("Location"))
	assert.Contains(t, anon.do("GET", page, nil, false).Body.String(), "1,500")
	for _, bad := range []string{"", "0", "abc"} {
		rec = ll.do("POST", "/listings/"+id+"/price", url.Values{"rent": {bad}}, false)
		assert.Equal(t, "/listings?done=price_error", rec.Header().Get("Location"), bad)
	}
	assert.Contains(t, anon.do("GET", page, nil, false).Body.String(), "1,500", "a bad edit changes nothing")
	assert.Equal(t, http.StatusNotFound, other.do("POST", "/listings/"+id+"/price", url.Values{"rent": {"10"}}, false).Code)
}
