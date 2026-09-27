package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/ent/user"
)

func TestSearchOverHTTP(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	ll := signInAs(t, h, d, capture, "0244000021", "Akua Owusu", "landlord")
	d.Ent.User.Update().Where(user.Phone("+233244000021")).SetIdentityVerifiedAt(time.Now()).ExecX(ctx)

	var ids []string
	for i := range 2 {
		rec := ll.do("POST", "/listings/new", url.Values{}, false)
		id := editRe.FindStringSubmatch(rec.Header().Get("Location"))[1]
		fillWizard(t, ll, id)
		for seed := range 3 {
			rec = ll.uploadPhotos("/listings/"+id+"/photos", [][]byte{photoJPEG(t, 60+i*10+seed)}, true)
			require.Equal(t, http.StatusOK, rec.Code)
		}
		if i == 0 {
			rec = ll.do("POST", "/listings/"+id+"/submit", url.Values{}, false)
			require.Equal(t, http.StatusSeeOther, rec.Code)
		}
		ids = append(ids, id) // the second stays a draft
	}
	live, draft := ids[0], ids[1]

	anon := newBrowser(t, h)
	rec := anon.do("GET", "/search", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	assert.Contains(t, page, "1 place in this area")
	assert.Contains(t, page, `data-result-id="`+live+`"`)
	assert.NotContains(t, page, draft)
	assert.Contains(t, page, "search.js")
	assert.Contains(t, page, `name="type" value="hostels"`)
	assert.NotContains(t, page, `content="noindex"`, "plain /search is indexable")

	// htmx gets only the results pane; filtered views aren't indexed.
	rec = anon.do("GET", "/search?type=apartments&max=100", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.HasPrefix(strings.TrimSpace(body), `<div id="search-results"`), body[:80])
	assert.Contains(t, body, "No apartments &amp; houses under ₵100 a month in this area")
	assert.Contains(t, body, "Clear filters")
	rec = anon.do("GET", "/search?type=hostels", nil, false)
	assert.Contains(t, rec.Body.String(), `content="noindex"`)
	assert.Contains(t, rec.Body.String(), `value="hostels" checked`)

	// Markers: GeoJSON on the approximate point only.
	rec = anon.do("GET", "/search/markers.geojson", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/geo+json", rec.Header().Get("Content-Type"))
	var fc struct {
		Features []struct {
			Geometry   struct{ Coordinates [2]float64 }
			Properties struct{ ID, Label string }
		}
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fc))
	require.Len(t, fc.Features, 1)
	assert.Equal(t, live, fc.Features[0].Properties.ID)
	assert.Equal(t, "₵3.2k/yr", fc.Features[0].Properties.Label)
	assert.NotContains(t, rec.Body.String(), "6.6697", "never the exact point")
	assert.NotContains(t, rec.Body.String(), "-1.5588")

	// Marker preview: live only.
	rec = anon.do("GET", "/l/"+live+"/card", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "2-in-a-room hostel near KNUST")
	assert.Contains(t, rec.Body.String(), "data-close-preview")
	assert.Equal(t, http.StatusNotFound, anon.do("GET", "/l/"+draft+"/card", nil, true).Code)

	// Home and the site header link to search.
	rec = anon.do("GET", "/", nil, false)
	assert.Contains(t, rec.Body.String(), `href="/search"`)
}
