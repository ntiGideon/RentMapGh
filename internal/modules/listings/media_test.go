package listings

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/platform/storage"
)

// testPhoto is a w×h JPEG of coloured blocks; different seeds give photos
// that are far apart by perceptual hash.
func testPhoto(t *testing.T, seed uint64, w, h int) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, seed*7+1))
	const n = 6
	var cells [n][n]color.RGBA
	for y := range cells {
		for x := range cells[y] {
			cells[y][x] = color.RGBA{uint8(r.IntN(256)), uint8(r.IntN(256)), uint8(r.IntN(256)), 255}
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, cells[y*n/h][x*n/w])
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	return buf.Bytes()
}

var photoSeed uint64 = 100

func addPhotos(t *testing.T, s *Service, a Actor, id uuid.UUID, n int) {
	t.Helper()
	for range n {
		photoSeed++
		_, err := s.AddPhoto(context.Background(), a, id, testPhoto(t, photoSeed, 900, 600))
		require.NoError(t, err)
	}
}

func photoIDs(t *testing.T, s *Service, a Actor, id uuid.UUID) []uuid.UUID {
	t.Helper()
	d, err := s.Load(context.Background(), a, id)
	require.NoError(t, err)
	var out []uuid.UUID
	for i, m := range d.Photos() {
		assert.Equal(t, i, m.Position, "positions stay 0..n-1")
		out = append(out, m.ID)
	}
	return out
}

func TestAddPhoto(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	store := s.media.(*storage.Memory)
	a := lister(t, c, false, "landlord")
	l, _ := s.CreateDraft(ctx, a)

	m, err := s.AddPhoto(ctx, a, l.ID, testPhoto(t, 1, 2400, 1800))
	require.NoError(t, err)
	assert.Equal(t, 0, m.Position)
	assert.Equal(t, [2]int{1600, 1200}, [2]int{m.Width, m.Height}, "largest rendition")
	assert.Len(t, m.Blurhash, 28)
	assert.Equal(t, len(PhotoSizes), store.Len(), "one object per size")
	for _, sz := range PhotoSizes {
		b, err := store.Get(ctx, MediaKey(m.ID, sz.File))
		require.NoError(t, err, sz.File)
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
		require.NoError(t, err)
		assert.Equal(t, sz.Max, cfg.Width, sz.File)
	}

	// The same photo again — even resized — is refused.
	var verr ValidationError
	_, err = s.AddPhoto(ctx, a, l.ID, testPhoto(t, 1, 1200, 900))
	require.ErrorAs(t, err, &verr)
	assert.Contains(t, verr["photos"], "already added")

	// Same blocks in portrait: the squashed hash matches, the shape doesn't → a different photo.
	portrait, err := s.AddPhoto(ctx, a, l.ID, testPhoto(t, 1, 900, 1200))
	require.NoError(t, err)
	require.NoError(t, s.DeletePhoto(ctx, a, l.ID, portrait.ID))

	_, err = s.AddPhoto(ctx, a, l.ID, testPhoto(t, 2, 600, 400))
	require.ErrorAs(t, err, &verr)
	assert.Contains(t, verr["photos"], "too small")

	_, err = s.AddPhoto(ctx, a, l.ID, []byte("%PDF-1.7 definitely not a photo"))
	require.ErrorAs(t, err, &verr)
	assert.Contains(t, verr["photos"], "JPEG, PNG or WebP")
	assert.Equal(t, len(PhotoSizes), store.Len(), "nothing stored for refused uploads")

	m2, err := s.AddPhoto(ctx, a, l.ID, testPhoto(t, 3, 800, 600))
	require.NoError(t, err)
	assert.Equal(t, 1, m2.Position)
	assert.Equal(t, 800, m2.Width, "small originals aren't upscaled")

	d, _ := s.Load(ctx, a, l.ID)
	q := QualityOf(d)
	assert.Equal(t, 2, q.Photos)
	assert.Equal(t, d.Cover().ID, m.ID)

	// Someone else's listing looks like it doesn't exist.
	other := lister(t, c, false, "landlord")
	_, err = s.AddPhoto(ctx, other, l.ID, testPhoto(t, 4, 800, 600))
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPhotoLimit(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a := lister(t, c, false, "landlord")
	l, _ := s.CreateDraft(ctx, a)
	for i := range MaxPhotos {
		_, err := s.AddPhoto(ctx, a, l.ID, testPhoto(t, uint64(1000+i), 500, 500))
		require.NoError(t, err, i)
	}
	var verr ValidationError
	_, err := s.AddPhoto(ctx, a, l.ID, testPhoto(t, 5000, 500, 500))
	require.ErrorAs(t, err, &verr)
	assert.Contains(t, verr["photos"], "up to 30")
}

func TestReorderMoveAndDeletePhotos(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	store := s.media.(*storage.Memory)
	a := lister(t, c, false, "landlord")
	l, _ := s.CreateDraft(ctx, a)
	addPhotos(t, s, a, l.ID, 4)
	ids := photoIDs(t, s, a, l.ID) // A B C D
	A, B, C, D := ids[0], ids[1], ids[2], ids[3]

	require.NoError(t, s.MovePhoto(ctx, a, l.ID, C, "cover"))
	assert.Equal(t, []uuid.UUID{C, A, B, D}, photoIDs(t, s, a, l.ID))
	require.NoError(t, s.MovePhoto(ctx, a, l.ID, D, "left"))
	assert.Equal(t, []uuid.UUID{C, A, D, B}, photoIDs(t, s, a, l.ID))
	require.NoError(t, s.MovePhoto(ctx, a, l.ID, C, "left"), "already first: no-op")
	require.NoError(t, s.MovePhoto(ctx, a, l.ID, B, "right"), "already last: no-op")
	assert.ErrorIs(t, s.MovePhoto(ctx, a, l.ID, C, "sideways"), ErrNotFound)

	// A stale order from another tab: unknown IDs ignored, missing ones kept at the end.
	require.NoError(t, s.ReorderPhotos(ctx, a, l.ID, []uuid.UUID{B, uuid.New(), A, B}))
	assert.Equal(t, []uuid.UUID{B, A, C, D}, photoIDs(t, s, a, l.ID))

	require.NoError(t, s.DeletePhoto(ctx, a, l.ID, B))
	assert.Equal(t, []uuid.UUID{A, C, D}, photoIDs(t, s, a, l.ID))
	assert.Equal(t, 3*len(PhotoSizes), store.Len(), "B's files are gone")
	_, err := store.Get(ctx, MediaKey(B, "w800.jpg"))
	assert.ErrorIs(t, err, storage.ErrNotFound)
	assert.ErrorIs(t, s.DeletePhoto(ctx, a, l.ID, B), ErrNotFound)

	other := lister(t, c, false, "landlord")
	assert.ErrorIs(t, s.DeletePhoto(ctx, other, l.ID, A), ErrNotFound)
	assert.ErrorIs(t, s.ReorderPhotos(ctx, other, l.ID, []uuid.UUID{D, C, A}), ErrNotFound)
	assert.ErrorIs(t, s.MovePhoto(ctx, other, l.ID, D, "cover"), ErrNotFound)
}

func TestPhotosGatePublishing(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a := lister(t, c, true, "landlord")
	l, _ := s.CreateDraft(ctx, a)
	for _, st := range fullSteps {
		if st.step == "photos" {
			addPhotos(t, s, a, l.ID, MinPhotos-1)
			_, errs, err := s.SaveStep(ctx, a, l.ID, "photos", url.Values{}, true)
			require.NoError(t, err)
			assert.Contains(t, errs["photos"], "at least 3")
			// "Add photos later" moves on without them.
			_, errs, err = s.SaveStep(ctx, a, l.ID, "photos", url.Values{"later": {"1"}}, true)
			require.NoError(t, err)
			assert.Nil(t, errs)
			continue
		}
		_, errs, err := s.SaveStep(ctx, a, l.ID, st.step, st.form, true)
		require.NoError(t, err)
		require.Nil(t, errs, st.step)
	}
	d, _ := s.Load(ctx, a, l.ID)
	require.Equal(t, []Requirement{{"photos", "Add at least 3 photos (2 so far)"}}, Missing(d))
	_, err := s.Submit(ctx, a, l.ID)
	assert.ErrorIs(t, err, ErrIncomplete)

	before := d.L.QualityScore
	addPhotos(t, s, a, l.ID, 1)
	d, _ = s.Load(ctx, a, l.ID)
	assert.Empty(t, Missing(d))
	assert.Equal(t, before, d.L.QualityScore, "3 photos publish; the 25 points come at 8")
	addPhotos(t, s, a, l.ID, GoodPhotos-MinPhotos)
	d, _ = s.Load(ctx, a, l.ID)
	assert.Equal(t, before+25, d.L.QualityScore)
	st, err := s.Submit(ctx, a, l.ID)
	require.NoError(t, err)
	assert.Equal(t, Active, st)
}
