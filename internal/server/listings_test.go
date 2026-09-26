package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/ent/listing"
)

var editRe = regexp.MustCompile(`/listings/([0-9a-f-]{36})/edit/`)

func TestListingWizardOverHTTP(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	ll := signInAs(t, h, d, capture, "0242222222", "Kofi", "landlord")

	rec := ll.do("GET", "/listings", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "List your first place")

	rec = ll.do("POST", "/listings/new", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	m := editRe.FindStringSubmatch(rec.Header().Get("Location"))
	require.Len(t, m, 2)
	base := "/listings/" + m[1] + "/edit/"

	// The location step loads the map module; skipping ahead is refused.
	rec = ll.do("GET", base+"location", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "location-picker.js")
	assert.Contains(t, rec.Body.String(), "maplibre-gl.css")
	rec = ll.do("GET", base+"pricing", nil, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, base+"location", rec.Header().Get("Location"))

	// Continue without a pin → 422 with the message, typed values kept.
	rec = ll.do("POST", base+"location", url.Values{"lat": {""}, "lng": {""}, "landmark": {"Blue gate"}}, false)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "Drop the pin")
	assert.Contains(t, rec.Body.String(), `value="Blue gate"`)

	steps := []struct {
		step string
		form url.Values
	}{
		{"location", url.Values{"lat": {"6.66970"}, "lng": {"-1.55880"}, "landmark": {"Blue gate"}, "neighbourhood": {""}, "digital_address": {""}, "street": {""}}},
		{"property", url.Values{"category": {"hostel"}, "name": {"Adom Hostel"}}},
		{"unit", url.Values{"unit_type": {"hostel_2in1"}, "furnished": {"full"}, "self_contained": {"yes"}, "meter_type": {"prepaid_shared"},
			"water_source": {"borehole"}, "kitchen": {"shared"}, "bathrooms": {"1"}, "size_sqm": {""}, "floor": {"1"}, "label": {"Room 12"}}},
		{"amenities", url.Values{"amenities": {"wifi", "study_room", "watchman"}}},
		{"photos", url.Values{}},
		{"pricing", url.Values{"rent": {"3,200"}, "rent_period": {"academic_year"}, "advance_periods": {"1"}, "deposit": {"0"}, "agent_fee": {"0"},
			"service_charge": {"150"}, "viewing_fee": {"0"}, "min_lease_months": {""}, "fee_label_1": {"Key deposit"}, "fee_amount_1": {"50"}}},
		{"details", url.Values{"headline": {"2-in-a-room hostel, 3 min walk to KNUST"}, "description": {"Borehole water, study room, fast Wi-Fi."}, "available_from": {""}}},
	}
	for i, st := range steps {
		if st.step == "photos" {
			rec = ll.do("POST", base+"photos", url.Values{}, false)
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "photos first")
			assert.Contains(t, rec.Body.String(), "Add at least 3 photos")
			for seed := range 3 {
				rec = ll.uploadPhotos("/listings/"+m[1]+"/photos", [][]byte{photoJPEG(t, seed)}, true)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			}
		}
		rec = ll.do("POST", base+st.step, st.form, false)
		require.Equal(t, http.StatusSeeOther, rec.Code, "%s: %s", st.step, rec.Body.String())
		next := "review"
		if i+1 < len(steps) {
			next = steps[i+1].step
		}
		assert.Equal(t, base+next, rec.Header().Get("Location"), st.step)
	}

	// Autosave on pricing returns the save indicator plus the live preview (out of band).
	rec = ll.do("POST", base+"pricing/autosave", url.Values{"rent": {"3,400"}, "rent_period": {"academic_year"}, "advance_periods": {"1"},
		"deposit": {"0"}, "agent_fee": {"0"}, "service_charge": {"150"}, "viewing_fee": {"0"}, "fee_label_1": {"Key deposit"}, "fee_amount_1": {"50"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `id="save-status"`)
	assert.Contains(t, body, `hx-swap-oob="true"`)
	assert.Contains(t, body, "GH₵ 3,600", "3,400 + 150 + 50")

	rec = ll.do("GET", base+"review", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	assert.Contains(t, body, "Submit for review", "unverified lister")
	assert.NotContains(t, body, "Before you can publish")

	rec = ll.do("POST", "/listings/"+m[1]+"/submit", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/listings?done=pending_review", rec.Header().Get("Location"))
	n, err := d.Ent.Listing.Query().Where(listing.StatusEQ(listing.StatusPendingReview)).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// A moderator sees it in the queue, with the exact pin, and approves it.
	mod := signInAs(t, h, d, capture, "0244444444", "Mo", "renter", "moderator")
	rec = mod.do("GET", "/admin/listings", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "2-in-a-room hostel")
	rec = mod.do("GET", "/admin/listings/"+m[1], nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "6.66970, -1.55880")
	rec = mod.do("POST", "/admin/listings/"+m[1]+"/decision", url.Values{"decision": {"approve"}}, false)
	require.Equal(t, "/admin/listings?done=approve", rec.Header().Get("Location"))

	rec = ll.do("GET", "/listings", nil, false)
	assert.Contains(t, rec.Body.String(), "Live")
	assert.Contains(t, rec.Body.String(), "GH₵ 3,400")
}

func TestListingAccessControl(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	owner := signInAs(t, h, d, capture, "0242222222", "Kofi", "landlord")
	other := signInAs(t, h, d, capture, "0243333333", "Ama", "landlord")
	renter := signInAs(t, h, d, capture, "0241111111", "Rita", "renter")

	rec := owner.do("POST", "/listings/new", url.Values{}, false)
	id := editRe.FindStringSubmatch(rec.Header().Get("Location"))[1]

	assert.Equal(t, http.StatusForbidden, renter.do("GET", "/listings", nil, false).Code)
	assert.Equal(t, http.StatusForbidden, renter.do("POST", "/listings/new", url.Values{}, false).Code)
	assert.Equal(t, http.StatusNotFound, other.do("GET", "/listings/"+id+"/edit/location", nil, false).Code)
	assert.Equal(t, http.StatusNotFound, other.do("POST", "/listings/"+id+"/edit/details/autosave", url.Values{"headline": {"Stolen"}}, true).Code)
	assert.Equal(t, http.StatusForbidden, owner.do("GET", "/admin/listings", nil, false).Code)
	assert.Equal(t, http.StatusNotFound, owner.do("GET", "/listings/not-a-uuid/edit/location", nil, false).Code)
	assert.Equal(t, http.StatusNotFound, owner.do("GET", "/listings/"+id+"/edit/nonsense", nil, false).Code)

	// The other landlord's list doesn't include it.
	assert.False(t, strings.Contains(other.do("GET", "/listings", nil, false).Body.String(), id))
}

// photoJPEG is a distinct 900×600 test photo per seed.
func photoJPEG(t *testing.T, seed int) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(uint64(seed), 99)) //nolint:gosec // G115: test seed
	img := image.NewRGBA(image.Rect(0, 0, 900, 600))
	var cells [6][6]color.RGBA
	for y := range cells {
		for x := range cells[y] {
			cells[y][x] = color.RGBA{uint8(r.IntN(256)), uint8(r.IntN(256)), uint8(r.IntN(256)), 255} //nolint:gosec // G115: < 256
		}
	}
	for y := 0; y < 600; y++ {
		for x := 0; x < 900; x++ {
			img.SetRGBA(x, y, cells[y/100][x/150])
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

// withGPS splices an EXIF segment with a GPS marker into a JPEG, like a
// phone camera does.
func withGPS(j []byte) []byte {
	payload := append([]byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08"), []byte("GPS 6.669700 N -1.558800 W")...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2)) //nolint:gosec // G115: tiny test segment
	out := append([]byte{}, j[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, j[2:]...)
}

// uploadPhotos posts photos like photo-manager.js (script=true: HX-Request +
// X-Photo-Upload) or like the plain no-JS form.
func (b *browser) uploadPhotos(target string, photos [][]byte, script bool) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for i, p := range photos {
		fw, _ := mw.CreateFormFile("photo", "IMG_"+strconv.Itoa(i)+".jpg")
		_, _ = fw.Write(p)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if script {
		req.Header.Set("HX-Request", "true")
		req.Header.Set("X-Photo-Upload", "1")
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	return rec
}

var mediaRe = regexp.MustCompile(`/media/([0-9a-f-]{36})/w320\.jpg`)

func TestListingPhotosOverHTTP(t *testing.T) {
	h, d, capture, media := newTestServerMedia(t)
	ctx := context.Background()
	ll := signInAs(t, h, d, capture, "0242222222", "Kofi", "landlord")
	other := signInAs(t, h, d, capture, "0243333333", "Ama", "landlord")
	rec := ll.do("POST", "/listings/new", url.Values{}, false)
	id := editRe.FindStringSubmatch(rec.Header().Get("Location"))[1]
	photos := "/listings/" + id + "/photos"

	// Script upload: the refreshed grid comes back.
	rec = ll.uploadPhotos(photos, [][]byte{withGPS(photoJPEG(t, 1))}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, `id="photo-grid"`)
	assert.Contains(t, body, "1 photo")
	assert.Contains(t, body, "Cover")
	first := mediaRe.FindStringSubmatch(body)
	require.Len(t, first, 2)

	// Refusals come back as plain text for that file's tile.
	rec = ll.uploadPhotos(photos, [][]byte{photoJPEG(t, 1)}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "You've already added this photo.", rec.Body.String())
	rec = ll.uploadPhotos(photos, [][]byte{[]byte("GIF89a")}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "JPEG, PNG or WebP")

	// The no-JS form: several at once, then back to the step.
	rec = ll.uploadPhotos(photos, [][]byte{photoJPEG(t, 2), photoJPEG(t, 3)}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/listings/"+id+"/edit/photos", rec.Header().Get("Location"))
	n, err := d.Ent.ListingMedia.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, 9, media.Len())

	// Served publicly, cached forever, with the camera's EXIF (and GPS) gone.
	for _, file := range []string{"w320.jpg", "w800.jpg", "w1600.jpg"} {
		rec = newBrowser(t, h).do("GET", "/media/"+first[1]+"/"+file, nil, false)
		require.Equal(t, http.StatusOK, rec.Code, file)
		assert.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
		assert.NotContains(t, rec.Body.String(), "Exif")
		assert.NotContains(t, rec.Body.String(), "GPS")
	}
	assert.Equal(t, http.StatusNotFound, ll.do("GET", "/media/"+first[1]+"/original.jpg", nil, false).Code)
	assert.Equal(t, http.StatusNotFound, ll.do("GET", "/media/not-a-uuid/w320.jpg", nil, false).Code)

	// The step page shows the manager with its script (once the lister gets there).
	rec = ll.do("GET", "/listings/"+id+"/edit/photos", nil, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code, "can't skip ahead to photos")
	_, err = d.Ent.Listing.UpdateOneID(uuid.MustParse(id)).SetWizardStep("photos").Save(ctx)
	require.NoError(t, err)
	rec = ll.do("GET", "/listings/"+id+"/edit/photos", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	assert.Contains(t, body, "photo-manager.js")
	assert.Contains(t, body, "3 photos")
	assert.Contains(t, body, `enctype="multipart/form-data"`)

	// Buttons (htmx): make the last photo the cover, reorder, delete.
	var ids []string
	for _, m := range mediaRe.FindAllStringSubmatch(body, -1) {
		if !slices.Contains(ids, m[1]) { // src and srcset
			ids = append(ids, m[1])
		}
	}
	require.Len(t, ids, 3)
	rec = ll.do("POST", photos+"/"+ids[2]+"/cover", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, ids[2], mediaRe.FindStringSubmatch(rec.Body.String())[1], "now first")
	rec = ll.do("POST", photos+"/order", url.Values{"order": {ids[0], ids[1], ids[2]}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, ids[0], mediaRe.FindStringSubmatch(rec.Body.String())[1])
	rec = ll.do("POST", photos+"/"+ids[0]+"/delete", url.Values{}, false)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, 6, media.Len())
	assert.Equal(t, http.StatusNotFound, ll.do("GET", "/media/"+ids[0]+"/w320.jpg", nil, false).Code)

	// Another landlord can't touch them.
	assert.Equal(t, http.StatusNotFound, other.uploadPhotos(photos, [][]byte{photoJPEG(t, 9)}, true).Code)
	assert.Equal(t, http.StatusNotFound, other.do("POST", photos+"/"+ids[1]+"/delete", url.Values{}, true).Code)
	assert.Equal(t, http.StatusNotFound, other.do("POST", photos+"/order", url.Values{"order": {ids[2], ids[1]}}, true).Code)
	assert.Equal(t, 6, media.Len())
}
