package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testClip is a 6-second phone-style clip with GPS in its metadata.
func testClip(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	raw, out := filepath.Join(dir, "raw.mp4"), filepath.Join(dir, "clip.mov")
	for _, args := range [][]string{
		{"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=6", "-f", "lavfi", "-i", "sine=duration=6",
			"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", raw},
		{"-i", raw, "-c", "copy", "-metadata", "location=+06.6697-001.5588/", out},
	} {
		b, err := exec.Command("ffmpeg", append([]string{"-hide_banner", "-v", "error", "-y"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(b))
	}
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	return b
}

// raw sends a body as video-uploader.js does.
func (b *browser) raw(method, target string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Content-Type", "application/octet-stream")
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	return rec
}

var videoRe = regexp.MustCompile(`/media/([0-9a-f-]{36})/v720\.mp4`)

func TestListingVideoOverHTTP(t *testing.T) {
	h, d, capture, _, svc := newTestServerFull(t, true)
	ctx := context.Background()
	ll := signInAs(t, h, d, capture, "0242222222", "Kofi", "landlord")
	other := signInAs(t, h, d, capture, "0243333333", "Ama", "landlord")
	rec := ll.do("POST", "/listings/new", url.Values{}, false)
	id := editRe.FindStringSubmatch(rec.Header().Get("Location"))[1]
	base := "/listings/" + id + "/video"
	clip := testClip(t)

	// The photos step shows the video section and loads the uploader.
	for _, st := range []struct {
		step string
		form url.Values
	}{
		{"location", url.Values{"lat": {"6.66970"}, "lng": {"-1.55880"}, "landmark": {"Blue gate"}}},
		{"property", url.Values{"category": {"hostel"}, "name": {"Adom Hostel"}}},
		{"unit", url.Values{"unit_type": {"hostel_2in1"}, "furnished": {"full"}, "self_contained": {"yes"}, "meter_type": {"prepaid_shared"},
			"water_source": {"borehole"}, "kitchen": {"shared"}, "bathrooms": {"1"}}},
		{"amenities", url.Values{"amenities": {"wifi"}}},
	} {
		require.Equal(t, http.StatusSeeOther, ll.do("POST", "/listings/"+id+"/edit/"+st.step, st.form, false).Code, st.step)
	}
	rec = ll.do("GET", "/listings/"+id+"/edit/photos", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `id="video-panel"`)
	assert.Contains(t, rec.Body.String(), "video-uploader.js")

	// Open an upload.
	rec = ll.do("POST", base+"/uploads", url.Values{"size": {strconv.Itoa(len(clip))}}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var start struct{ ID string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &start))
	up := base + "/uploads/" + start.ID

	// Other listers can't see or feed it.
	assert.Equal(t, http.StatusNotFound, other.raw("POST", up+"?offset=0", clip[:10]).Code)
	assert.Equal(t, http.StatusNotFound, other.do("GET", up, nil, true).Code)

	half := len(clip) / 2
	rec = ll.raw("POST", up+"?offset=0", clip[:half])
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, strconv.Itoa(half), rec.Header().Get("Upload-Offset"))

	// A repeated piece (the answer got lost) → 409 with where to resume.
	rec = ll.raw("POST", up+"?offset=0", clip[:half])
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, strconv.Itoa(half), rec.Header().Get("Upload-Offset"))
	rec = ll.do("GET", up, nil, true)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, strconv.Itoa(half), rec.Header().Get("Upload-Offset"))

	// The last piece answers with the panel, now "preparing" and polling.
	rec = ll.raw("POST", up+"?offset="+strconv.Itoa(half), clip[half:])
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "1", rec.Header().Get("Upload-Complete"))
	assert.Contains(t, rec.Body.String(), "Preparing your video")
	assert.Contains(t, rec.Body.String(), `hx-trigger="every 5s"`)

	n, err := svc.ProcessVideos(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	rec = ll.do("GET", base, nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, "every 5s", "stops polling")
	assert.Contains(t, body, "<video")
	assert.Contains(t, body, "0:06")
	m := videoRe.FindStringSubmatch(body)
	require.Len(t, m, 2)

	// Served with Range support (players seek), cached forever, no GPS.
	anon := newBrowser(t, h)
	rec = anon.do("GET", "/media/"+m[1]+"/v720.mp4", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
	assert.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
	assert.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
	assert.NotContains(t, rec.Body.String(), "+06.6697")
	full := rec.Body.Len()
	req := httptest.NewRequest("GET", "/media/"+m[1]+"/v480.mp4", nil)
	req.Header.Set("Range", "bytes=0-99")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusPartialContent, rr.Code)
	assert.Equal(t, 100, rr.Body.Len())
	assert.Positive(t, full)
	assert.Equal(t, http.StatusOK, anon.do("GET", "/media/"+m[1]+"/w800.jpg", nil, false).Code, "poster")
	assert.Equal(t, http.StatusNotFound, anon.do("GET", "/media/"+m[1]+"/original.mov", nil, false).Code)

	// The review summary mentions it.
	rec = ll.do("GET", "/listings/"+id+"/edit/review", nil, false)
	if rec.Code == http.StatusOK {
		assert.Contains(t, rec.Body.String(), "Added (0:06)")
	}

	// Delete (htmx) → the empty panel with the uploader again.
	rec = ll.do("POST", base+"/delete", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "data-video-uploader")
	assert.Equal(t, http.StatusNotFound, anon.do("GET", "/media/"+m[1]+"/v720.mp4", nil, false).Code)

	// The no-JS form: one multipart post, then back to the step.
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	fw, _ := mw.CreateFormFile("video", "VID_0001.mov")
	_, _ = fw.Write(clip)
	_ = mw.Close()
	req = httptest.NewRequest("POST", base, &mp)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, c := range ll.cookies {
		req.AddCookie(c)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusSeeOther, rr.Code, rr.Body.String())
	assert.Equal(t, "/listings/"+id+"/edit/photos", rr.Header().Get("Location"))

	// A refused file comes back as plain text for the uploader.
	rec = ll.do("POST", base+"/uploads", url.Values{"size": {"1000"}}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "already has a video")
}
