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

	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/availability"
	"rentmapgh/internal/platform/sms"
)

var confirmLinkRe = regexp.MustCompile(`http://example\.test(/c/[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+)`)

func publicPage(t *testing.T, b *browser, id string) string {
	t.Helper()
	rec := b.do("GET", "/l/"+id, nil, false)
	if rec.Code == http.StatusMovedPermanently {
		rec = b.do("GET", rec.Header().Get("Location"), nil, false)
	}
	return rec.Body.String()
}

func TestAvailabilityOverHTTP(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	ll, ids := liveListingsBy(t, h, d, capture, "0244000091", 2)
	a, b := ids[0], ids[1]
	anon := newBrowser(t, h)
	assert.Contains(t, publicPage(t, anon, a), "Available · confirmed just now")
	assert.Contains(t, publicPage(t, anon, a), "Already rented? Tell us")

	// A renter says it's rented: the badge changes and the landlord gets a link.
	renter := signInAs(t, h, d, capture, "0244000092", "Kofi", "renter")
	rec := renter.do("POST", "/l/"+a+"/rented-report", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "asked the lister")
	assert.Contains(t, publicPage(t, anon, a), "A renter says it may be rented")
	// Texts wait for daytime (Ghana is GMT); the link is the same either way.
	if hr := time.Now().UTC().Hour(); hr >= 7 && hr < 20 {
		sms := capture.Last()
		require.Equal(t, "+233244000091", sms.To)
		require.Regexp(t, confirmLinkRe, sms.Body)
	}
	signer := availability.NewService(d.Ent, audit.New(d.Ent), &sms.Capture{}, nil, "test-secret-test-secret-test-secret", "http://example.test")
	link := confirmLinkRe.FindStringSubmatch(signer.Link(uuid.MustParse(a)))[1]

	// The link opens a page (no login) and changes nothing by itself.
	phone := newBrowser(t, h)
	rec = phone.do("GET", link, nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Is this place still available?")
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	assert.Equal(t, listing.StatusActive, d.Ent.Listing.GetX(ctx, uuid.MustParse(a)).Status)

	// "It's rented" asks the question before changing anything.
	rec = phone.do("POST", link, url.Values{"answer": {"rented"}}, false)
	assert.Contains(t, rec.Body.String(), "Did you find your tenant through RentMap?")
	assert.Equal(t, listing.StatusActive, d.Ent.Listing.GetX(ctx, uuid.MustParse(a)).Status)
	rec = phone.do("POST", link, url.Values{"answer": {"rented"}, "via": {"rentmap"}}, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Congratulations")
	got := d.Ent.Listing.GetX(ctx, uuid.MustParse(a))
	assert.Equal(t, listing.StatusRented, got.Status)
	assert.Equal(t, listing.RentedViaRentmap, *got.RentedVia)

	// A forged or broken link is refused.
	rec = phone.do("GET", link[:len(link)-3]+"abc", nil, false)
	assert.Equal(t, http.StatusGone, rec.Code)

	// "Mark rented" in Your listings goes to the question first.
	rec = ll.do("POST", "/listings/"+b+"/actions/mark_rented", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/listings/"+b+"/rented", rec.Header().Get("Location"))
	assert.Contains(t, ll.do("GET", "/listings/"+b+"/rented", nil, false).Body.String(), "Did you find your tenant through RentMap?")
	assert.Equal(t, http.StatusForbidden, renter.do("GET", "/listings/"+b+"/rented", nil, false).Code, "only listers")
	rec = ll.do("POST", "/listings/"+b+"/rented", url.Values{"answer": {"rented"}, "via": {"elsewhere"}}, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, listing.RentedViaElsewhere, *d.Ent.Listing.GetX(ctx, uuid.MustParse(b)).RentedVia)

	// A stale live listing offers "Still available" in Your listings.
	rec = ll.do("POST", "/listings/"+b+"/actions/relist", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	d.Ent.Listing.UpdateOneID(uuid.MustParse(b)).SetLastConfirmedAt(time.Now().Add(-5 * 24 * time.Hour)).ExecX(ctx)
	assert.Contains(t, publicPage(t, anon, b), "Not recently confirmed")
	mine := ll.do("GET", "/listings", nil, false).Body.String()
	assert.Contains(t, mine, "Not confirmed recently")
	rec = ll.do("POST", "/listings/"+b+"/actions/confirm", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/listings?done=confirmed", rec.Header().Get("Location"))
	assert.Contains(t, publicPage(t, anon, b), "Available · confirmed just now")

	// Expired long enough ago: the public page closes (the lister still sees it).
	d.Ent.Listing.UpdateOneID(uuid.MustParse(b)).SetStatus(listing.StatusExpired).
		SetLastConfirmedAt(time.Now().Add(-50 * 24 * time.Hour)).ExecX(ctx)
	assert.Equal(t, http.StatusNotFound, anon.do("GET", "/l/"+b, nil, false).Code)
	assert.NotEqual(t, http.StatusNotFound, ll.do("GET", "/l/"+b, nil, false).Code)
}
