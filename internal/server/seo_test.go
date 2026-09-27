package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ldRe = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

func TestSEOPages(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ids := liveListings(t, h, d, capture, "0244000041", 2) // hostels in Ayeduase (fillWizard)
	anon := newBrowser(t, h)

	rec := anon.do("GET", "/kumasi/knust/hostels", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	assert.Contains(t, page, "Hostels near KNUST, Kumasi")
	assert.Contains(t, page, "2 hostels near KNUST, Kumasi, from GH₵ 300 to GH₵ 400 a month", "academic-year rents shown per month")
	assert.Contains(t, page, `data-result-id="`+ids[0]+`"`)
	assert.Contains(t, page, `href="/kumasi/knust/rooms-for-rent"`, "other kinds here")
	assert.Contains(t, page, `href="/kumasi/tech-junction/hostels"`, "nearby areas")
	assert.Contains(t, page, `/search?near=knust&amp;radius=2&amp;type=hostels`)
	assert.NotContains(t, page, `content="noindex"`)
	assert.Contains(t, page, `rel="canonical" href="http://example.test/kumasi/knust/hostels"`)
	m := ldRe.FindStringSubmatch(page)
	require.Len(t, m, 2)
	var ld []map[string]any
	require.NoError(t, json.Unmarshal([]byte(m[1]), &ld))
	assert.Equal(t, "ItemList", ld[0]["@type"])
	assert.EqualValues(t, 2, ld[0]["numberOfItems"])

	// Nothing there: still a page, but not indexed.
	rec = anon.do("GET", "/kumasi/tanoso/shops-for-rent", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Nothing listed here yet")
	assert.Contains(t, rec.Body.String(), `content="noindex"`)
	assert.Equal(t, http.StatusNotFound, anon.do("GET", "/kumasi/atlantis/hostels", nil, false).Code)
	assert.Equal(t, http.StatusNotFound, anon.do("GET", "/kumasi/knust/castles", nil, false).Code)

	// The listing page describes itself for search engines — by town, never coordinates.
	rec = anon.do("GET", "/l/"+ids[0], nil, false)
	rec = anon.do("GET", rec.Header().Get("Location"), nil, false)
	m = ldRe.FindStringSubmatch(rec.Body.String())
	require.Len(t, m, 2)
	var offer map[string]any
	require.NoError(t, json.Unmarshal([]byte(m[1]), &offer))
	assert.Equal(t, "Offer", offer["@type"])
	assert.Equal(t, "GHS", offer["priceCurrency"])
	assert.Equal(t, "3200.00", offer["price"])
	assert.NotContains(t, m[1], "latitude")
	assert.NotContains(t, m[1], "geo")

	// Sitemap and robots.
	rec = anon.do("GET", "/sitemap.xml", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	sm := rec.Body.String()
	assert.Contains(t, sm, "<loc>http://example.test/kumasi/knust/hostels</loc>")
	assert.NotContains(t, sm, "/kumasi/tanoso/shops-for-rent", "empty pages stay out")
	assert.Contains(t, sm, "/l/"+ids[1]+"/")
	assert.Equal(t, 2, strings.Count(sm, "<lastmod>"))
	rec = anon.do("GET", "/robots.txt", nil, false)
	assert.Contains(t, rec.Body.String(), "Sitemap: http://example.test/sitemap.xml")
	assert.Contains(t, rec.Body.String(), "Disallow: /admin/")
}
