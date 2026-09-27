package server

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/user"
)

func TestPublicListingPage(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	ll := signInAs(t, h, d, capture, "0244000011", "Akua Owusu", "landlord")
	rec := ll.do("POST", "/listings/new", url.Values{}, false)
	id := editRe.FindStringSubmatch(rec.Header().Get("Location"))[1]
	fillWizard(t, ll, id)
	// The private details: exact pin, digital address, street, landmark.
	rec = ll.do("POST", "/listings/"+id+"/edit/location", url.Values{"lat": {"6.66970"}, "lng": {"-1.55880"}, "neighbourhood": {"ayeduase"},
		"landmark": {"Behind the Zongo mosque"}, "digital_address": {"AK-039-5028"}, "street": {"Lagos Avenue"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	for seed := range 3 {
		rec = ll.uploadPhotos("/listings/"+id+"/photos", [][]byte{photoJPEG(t, seed+40)}, true)
		require.Equal(t, http.StatusOK, rec.Code)
	}
	lid := uuid.MustParse(id)

	// Not public while it's a draft: 404 for strangers, a preview for the lister.
	anon := newBrowser(t, h)
	assert.Equal(t, http.StatusNotFound, anon.do("GET", "/l/"+id, nil, false).Code)
	rec = ll.do("GET", "/l/"+id, nil, false)
	require.Equal(t, http.StatusMovedPermanently, rec.Code)
	canonical := rec.Header().Get("Location")
	assert.True(t, strings.HasPrefix(canonical, "/l/"+id+"/2-in-a-room-hostel-near-knust"), canonical)
	rec = ll.do("GET", canonical, nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Preview")
	assert.Contains(t, rec.Body.String(), `content="noindex"`)

	// Publish as an ID-verified landlord.
	d.Ent.User.Update().Where(user.Phone("+233244000011")).SetIdentityVerifiedAt(time.Now()).ExecX(ctx)
	rec = ll.do("POST", "/listings/"+id+"/submit", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, listing.StatusActive, d.Ent.Listing.GetX(ctx, lid).Status)

	rec = anon.do("GET", "/l/"+id+"/old-slug", nil, false)
	assert.Equal(t, http.StatusMovedPermanently, rec.Code)
	assert.Equal(t, canonical, rec.Header().Get("Location"))

	rec = anon.do("GET", canonical, nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	assert.Contains(t, page, "2-in-a-room hostel near KNUST")
	assert.Contains(t, page, "Ayeduase")
	assert.Contains(t, page, "Adom Hostel", "hostel names are public")
	assert.Contains(t, page, "Total to move in")
	assert.Contains(t, page, "from KNUST")
	assert.Contains(t, page, "ID checked")
	assert.Contains(t, page, `property="og:image" content="http://example.test/media/`)
	assert.Contains(t, page, `rel="canonical" href="http://example.test`+canonical+`"`)
	assert.Contains(t, page, "https://wa.me/?text=")
	assert.NotContains(t, page, `content="noindex"`)
	assert.Contains(t, rec.Header().Get("Cache-Control"), "public")

	// §6.1: nothing that pins the building down.
	p := d.Ent.Property.Query().OnlyX(ctx)
	for _, secret := range []string{"6.6697", "-1.5588", "1.5588", "AK-039-5028", "AK0395028", "Lagos Avenue", "Zongo mosque"} {
		assert.NotContains(t, page, secret, "public page leaks %q", secret)
	}
	assert.Contains(t, page, `data-lat="`+strconv.FormatFloat(*p.ApproxLat, 'f', 5, 64)+`"`, "the map gets the approximate point")

	// Rented: the link keeps working, marked unavailable and not indexed.
	rec = ll.do("POST", "/listings/"+id+"/actions/mark_rented", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	rec = anon.do("GET", canonical, nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "has been rented")
	assert.Contains(t, rec.Body.String(), `content="noindex"`)

	// Withdrawn to review or removed: gone for strangers.
	d.Ent.Listing.UpdateOneID(lid).SetStatus(listing.StatusRemoved).ExecX(ctx)
	assert.Equal(t, http.StatusNotFound, anon.do("GET", canonical, nil, false).Code)
	assert.Equal(t, http.StatusNotFound, anon.do("GET", "/l/not-a-uuid", nil, false).Code)
}
