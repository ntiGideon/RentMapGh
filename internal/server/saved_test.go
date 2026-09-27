package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/platform/sms"
)

// liveListings publishes n listings from one ID-checked landlord and
// returns their IDs (the rent differs per listing).
func liveListings(t *testing.T, h http.Handler, d *db.DB, capture *sms.Capture, phone string, n int) []string {
	t.Helper()
	ctx := context.Background()
	ll := signInAs(t, h, d, capture, phone, "Akua Owusu", "landlord")
	e164 := "+233" + strings.TrimPrefix(phone, "0")
	d.Ent.User.Update().Where(user.Phone(e164)).SetIdentityVerifiedAt(time.Now()).ExecX(ctx)
	var ids []string
	for i := range n {
		rec := ll.do("POST", "/listings/new", url.Values{}, false)
		id := editRe.FindStringSubmatch(rec.Header().Get("Location"))[1]
		fillWizard(t, ll, id)
		rec = ll.do("POST", "/listings/"+id+"/edit/pricing", url.Values{"rent": {[]string{"3,200", "2,400", "4,000", "5,000", "6,000"}[i]},
			"rent_period": {"academic_year"}, "advance_periods": {"1"}, "deposit": {"0"}, "agent_fee": {"0"}, "service_charge": {"0"}, "viewing_fee": {"0"}}, false)
		require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
		for seed := range 3 {
			rec = ll.uploadPhotos("/listings/"+id+"/photos", [][]byte{photoJPEG(t, 100+i*10+seed)}, true)
			require.Equal(t, http.StatusOK, rec.Code)
		}
		rec = ll.do("POST", "/listings/"+id+"/submit", url.Values{}, false)
		require.Equal(t, http.StatusSeeOther, rec.Code)
		ids = append(ids, id)
	}
	return ids
}

func TestSavedAndCompare(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ids := liveListings(t, h, d, capture, "0244000031", 2)
	a, b := ids[0], ids[1]
	anon := newBrowser(t, h)

	rec := anon.do("GET", "/saved", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Nothing saved yet")

	// Heart over htmx: the new button plus a toast; the cookie remembers.
	rec = anon.do("POST", "/saved/"+a, url.Values{"back": {"/search"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `aria-pressed="true"`)
	assert.Contains(t, rec.Header().Get("HX-Trigger"), "Saved")
	ck := anon.cookies["saved"]
	require.NotNil(t, ck)
	assert.True(t, ck.HttpOnly)

	// No-script: back where it came from; open redirects refused.
	rec = anon.do("POST", "/saved/"+b, url.Values{"back": {"//evil.example/x"}}, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/saved", rec.Header().Get("Location"))

	rec = anon.do("GET", "/saved", nil, false)
	page := rec.Body.String()
	assert.Contains(t, page, "2 places on your shortlist")
	assert.Less(t, strings.Index(page, b), strings.Index(page, a), "newest first")
	assert.Contains(t, page, `data-compare="`+a+`"`)

	// Search and listing pages show the heart as saved.
	rec = anon.do("GET", "/search", nil, false)
	assert.Contains(t, rec.Body.String(), `data-save-form="`+a+`"`)
	assert.Equal(t, 2, strings.Count(rec.Body.String(), `aria-pressed="true"`))

	// Un-save.
	rec = anon.do("POST", "/saved/"+a, url.Values{}, true)
	assert.Contains(t, rec.Body.String(), `aria-pressed="false"`)
	assert.Contains(t, anon.do("GET", "/saved", nil, false).Body.String(), "1 place on your shortlist")

	// A tampered cookie is ignored, not trusted.
	anon.cookies["saved"].Value = "AAAAAAAAAAAAAAAAAAAAAA.bogus"
	assert.Contains(t, anon.do("GET", "/saved", nil, false).Body.String(), "Nothing saved yet")

	// Drafts can't be saved.
	assert.Equal(t, http.StatusNotFound, anon.do("POST", "/saved/01a0e3f0-0000-7000-8000-000000000000", url.Values{}, true).Code)

	// Compare: side by side, cheapest marked.
	rec = anon.do("GET", "/compare?ids="+a+","+b+",not-a-uuid", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	page = rec.Body.String()
	assert.Contains(t, page, "Total to move in")
	assert.Contains(t, page, "Per month")
	assert.Equal(t, 2, strings.Count(page, "Lowest"), "per month and move-in: the ₵2,400 place")
	assert.Contains(t, page, `content="noindex"`)
	rec = anon.do("GET", "/compare?ids="+a, nil, false)
	assert.Contains(t, rec.Body.String(), "Pick at least two places")
}
