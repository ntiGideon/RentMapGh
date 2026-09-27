package listings

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/ent/listingmedia"
	"rentmapgh/internal/platform/storage"
	"rentmapgh/internal/platform/video"
)

// setupVideo is setup plus videos, skipped without ffmpeg.
func setupVideo(t *testing.T) (*Service, Actor, uuid.UUID) {
	t.Helper()
	s, c := setup(t)
	tool, err := video.Find("", "")
	if err != nil {
		t.Skip("ffmpeg/ffprobe not on PATH")
	}
	require.NoError(t, s.EnableVideo(tool, t.TempDir()))
	a := lister(t, c, false, "landlord")
	l, err := s.CreateDraft(context.Background(), a)
	require.NoError(t, err)
	return s, a, l.ID
}

// phoneVideo is a clip like a phone records: landscape pixels with a 90°
// rotation flag, sound, and the building's GPS position in the metadata.
func phoneVideo(t *testing.T, seconds string) []byte {
	t.Helper()
	dir := t.TempDir()
	raw, out := filepath.Join(dir, "raw.mp4"), filepath.Join(dir, "phone.mov")
	run := func(args ...string) {
		b, err := exec.Command("ffmpeg", append([]string{"-hide_banner", "-v", "error", "-y"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(b))
	}
	run("-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration="+seconds, "-f", "lavfi", "-i", "sine=duration="+seconds,
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", raw)
	run("-display_rotation", "90", "-i", raw, "-c", "copy", "-metadata", "location=+06.6697-001.5588/", out)
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	return b
}

// uploadAll sends data in pieces of n bytes.
func uploadAll(t *testing.T, s *Service, a Actor, id uuid.UUID, data []byte, n int) (uuid.UUID, error) {
	t.Helper()
	ctx := context.Background()
	uid, err := s.StartVideoUpload(ctx, a, id, int64(len(data)))
	if err != nil {
		return uid, err
	}
	for off := 0; off < len(data); off += n {
		end := min(off+n, len(data))
		have, done, err := s.AppendVideoChunk(ctx, a, id, uid, int64(off), bytes.NewReader(data[off:end]))
		if err != nil {
			return uid, err
		}
		assert.Equal(t, int64(end), have)
		assert.Equal(t, end == len(data), done)
	}
	return uid, nil
}

func TestVideoUploadAndProcess(t *testing.T) {
	s, a, id := setupVideo(t)
	ctx := context.Background()
	store := s.media.(*storage.Memory)
	clip := phoneVideo(t, "6")
	require.True(t, bytes.Contains(clip, []byte("+06.6697")), "the test clip carries GPS")

	uid, err := s.StartVideoUpload(ctx, a, id, int64(len(clip)))
	require.NoError(t, err)

	// A piece that doesn't start where the upload stands is refused with the
	// right offset, and nothing is written.
	half := len(clip) / 2
	_, _, err = s.AppendVideoChunk(ctx, a, id, uid, 10, bytes.NewReader(clip[10:half]))
	assert.Equal(t, OffsetError{Have: 0}, err)

	// A connection that drops mid-piece keeps what arrived; the client asks
	// and resumes from there.
	have, done, err := s.AppendVideoChunk(ctx, a, id, uid, 0, &cutReader{data: clip[:half], cut: 1000})
	require.Error(t, err)
	assert.False(t, done)
	assert.Equal(t, int64(1000), have)
	off, err := s.VideoUploadOffset(a, id, uid)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), off)

	_, done, err = s.AppendVideoChunk(ctx, a, id, uid, off, bytes.NewReader(clip[off:half]))
	require.NoError(t, err)
	assert.False(t, done)
	_, done, err = s.AppendVideoChunk(ctx, a, id, uid, int64(half), bytes.NewReader(clip[half:]))
	require.NoError(t, err)
	assert.True(t, done)

	d, err := s.Load(ctx, a, id)
	require.NoError(t, err)
	v := d.Video()
	require.NotNil(t, v)
	assert.Equal(t, uid, v.ID)
	assert.Equal(t, listingmedia.StatusProcessing, v.Status)
	assert.Equal(t, [2]int{720, 1280}, [2]int{v.Width, v.Height}, "portrait from the rotation flag")
	assert.InDelta(t, 6000, *v.DurationMs, 200)
	assert.False(t, d.HasReadyVideo())
	assert.Empty(t, d.Photos(), "the video isn't a photo")
	before := d.L.QualityScore

	n, err := s.ProcessVideos(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = s.ProcessVideos(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left")

	d, err = s.Load(ctx, a, id)
	require.NoError(t, err)
	v = d.Video()
	assert.Equal(t, listingmedia.StatusReady, v.Status)
	assert.Equal(t, [2]int{720, 1280}, [2]int{v.Width, v.Height})
	assert.Len(t, v.Blurhash, 28)
	assert.NotZero(t, v.Phash)
	assert.Equal(t, before+5, d.L.QualityScore, "the video's quality points")
	assert.Equal(t, len(VideoOutputs)+len(PhotoSizes), store.Len())
	for _, f := range videoFiles() {
		b, err := store.Get(ctx, MediaKey(uid, f))
		require.NoError(t, err, f)
		assert.False(t, bytes.Contains(b, []byte("+06.6697")), "%s must not carry the GPS position", f)
	}
	entries, _ := os.ReadDir(s.inbox)
	assert.Empty(t, entries, "the original is gone")

	// One video per listing.
	_, err = s.StartVideoUpload(ctx, a, id, 1000)
	assert.Equal(t, ValidationError{"video": "This listing already has a video. Delete it to add another."}, err)

	// Photo actions don't touch the video.
	assert.ErrorIs(t, s.DeletePhoto(ctx, a, id, uid), ErrNotFound)
	assert.ErrorIs(t, s.MovePhoto(ctx, a, id, uid, "cover"), ErrNotFound)
	addPhotos(t, s, a, id, 2)
	ids := photoIDs(t, s, a, id)
	require.NoError(t, s.MovePhoto(ctx, a, id, ids[1], "cover"))
	assert.Equal(t, []uuid.UUID{ids[1], ids[0]}, photoIDs(t, s, a, id))

	// Delete removes the row and every file.
	require.NoError(t, s.DeleteVideo(ctx, a, id))
	d, _ = s.Load(ctx, a, id)
	assert.Nil(t, d.Video())
	assert.Equal(t, before, d.L.QualityScore, "the points go with it (2 photos earn none)")
	assert.Equal(t, 2*len(PhotoSizes), store.Len(), "only the photos remain")
}

// cutReader returns cut bytes, then fails like a dropped connection.
type cutReader struct {
	data []byte
	cut  int
	pos  int
}

func (r *cutReader) Read(p []byte) (int, error) {
	if r.pos >= r.cut {
		return 0, os.ErrDeadlineExceeded
	}
	n := copy(p, r.data[r.pos:r.cut])
	r.pos += n
	return n, nil
}

func TestVideoRefusals(t *testing.T) {
	s, a, id := setupVideo(t)
	ctx := context.Background()

	_, err := s.StartVideoUpload(ctx, a, id, MaxVideoBytes+1)
	assert.Contains(t, err.(ValidationError)["video"], "over 250 MB")
	_, err = s.StartVideoUpload(ctx, a, id, 0)
	assert.Error(t, err)

	// Not a video: refused when the last byte arrives, and nothing is kept.
	junk := bytes.Repeat([]byte("not a video "), 5000)
	_, err = uploadAll(t, s, a, id, junk, 20000)
	assert.Equal(t, ValidationError{"video": video.ErrNotVideo.Error()}, err)
	entries, _ := os.ReadDir(s.inbox)
	assert.Empty(t, entries)

	// Too short.
	_, err = uploadAll(t, s, a, id, phoneVideo(t, "2"), 1<<20)
	assert.Contains(t, err.(ValidationError)["video"], "too short")

	// Sending more than announced drops the upload.
	uid, err := s.StartVideoUpload(ctx, a, id, 100)
	require.NoError(t, err)
	_, _, err = s.AppendVideoChunk(ctx, a, id, uid, 0, bytes.NewReader(make([]byte, 101)))
	assert.Error(t, err)
	_, err = s.VideoUploadOffset(a, id, uid)
	assert.ErrorIs(t, err, ErrUploadGone)

	// Someone else's upload ID is unknown to them.
	uid, err = s.StartVideoUpload(ctx, a, id, 100)
	require.NoError(t, err)
	other := a
	other.UserID = uuid.Must(uuid.NewV7())
	_, err = s.VideoUploadOffset(other, id, uid)
	assert.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.AppendVideoChunk(ctx, other, id, uid, 0, bytes.NewReader(make([]byte, 10)))
	assert.ErrorIs(t, err, ErrNotFound)

	// Without ffmpeg the feature is off.
	off, _ := setup(t)
	assert.False(t, off.VideoEnabled())
	_, err = off.StartVideoUpload(ctx, a, id, 100)
	assert.ErrorIs(t, err, ErrVideoOff)
}

func TestVideoFailedAndTidy(t *testing.T) {
	s, a, id := setupVideo(t)
	ctx := context.Background()

	// A source that turns out unreadable ends "failed", not stuck.
	uid, err := uploadAll(t, s, a, id, phoneVideo(t, "6"), 1<<20)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(s.inboxPath(uid, ".src"), []byte("garbage"), 0o600))
	n, err := s.ProcessVideos(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	d, _ := s.Load(ctx, a, id)
	assert.Equal(t, listingmedia.StatusFailed, d.Video().Status)
	assert.False(t, d.HasReadyVideo())

	// The lister removes it and can try again.
	require.NoError(t, s.DeleteVideo(ctx, a, id))
	_, err = s.StartVideoUpload(ctx, a, id, 1000)
	require.NoError(t, err)

	// Uploads abandoned for a day are tidied away.
	old := time.Now().Add(-25 * time.Hour)
	entries, _ := os.ReadDir(s.inbox)
	require.NotEmpty(t, entries)
	for _, e := range entries {
		require.NoError(t, os.Chtimes(filepath.Join(s.inbox, e.Name()), old, old))
	}
	s.tidyInbox(ctx)
	entries, _ = os.ReadDir(s.inbox)
	assert.Empty(t, entries)
}
