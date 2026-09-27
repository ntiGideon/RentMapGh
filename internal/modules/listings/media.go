package listings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listingmedia"
	"rentmapgh/internal/platform/imaging"
)

// Photo rules. Publishing needs MinPhotos; the quality score wants GoodPhotos.
const (
	MinPhotos     = 3
	GoodPhotos    = 8
	MaxPhotos     = 30
	MinPhotoSide  = 480      // px on the short side, after rotation
	MaxPhotoBytes = 15 << 20 // per upload; phones shrink to ~0.5 MB before sending
	// dupDistance: pHash bits that may differ for "the same photo again".
	// Re-encoding the same photo at any of our sizes drifts by up to 4.
	dupDistance = 4
)

// PhotoSize is one stored rendition. Every photo gets all three, so a URL
// never 404s; small originals are simply not upscaled.
type PhotoSize struct {
	File    string
	Max     int // longest side
	Quality int
}

var PhotoSizes = []PhotoSize{
	{"w320.jpg", 320, 76},   // cards, thumbnails
	{"w800.jpg", 800, 80},   // listing page on phones
	{"w1600.jpg", 1600, 82}, // lightbox, desktop
}

// MediaKey is where a rendition lives in the media store.
func MediaKey(id uuid.UUID, file string) string { return "media/" + id.String() + "/" + file }

// MediaURL is the public URL of a rendition (served by Handler.Media).
func MediaURL(id uuid.UUID, file string) string { return "/media/" + id.String() + "/" + file }

// IsPhotoFile reports whether file is one of the rendition names.
func IsPhotoFile(file string) bool {
	return slices.ContainsFunc(PhotoSizes, func(s PhotoSize) bool { return s.File == file })
}

// Photos returns the listing's photos in display order (cover first).
func (d *Item) Photos() []*ent.ListingMedia {
	var out []*ent.ListingMedia
	for _, m := range d.M {
		if m.Kind == listingmedia.KindPhoto {
			out = append(out, m)
		}
	}
	return out
}

// Cover is the first photo, or nil.
func (d *Item) Cover() *ent.ListingMedia {
	if ps := d.Photos(); len(ps) > 0 {
		return ps[0]
	}
	return nil
}

func withMedia(q *ent.ListingMediaQuery) {
	q.Order(ent.Asc(listingmedia.FieldPosition), ent.Asc(listingmedia.FieldCreatedAt))
}

// AddPhoto validates, re-encodes and stores one photo at the end of the
// listing's gallery.
func (s *Service) AddPhoto(ctx context.Context, a Actor, id uuid.UUID, data []byte) (*ent.ListingMedia, error) {
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if !Editable(Status(d.L.Status)) {
		return nil, ValidationError{"photos": "This listing can't be edited."}
	}
	if len(d.Photos()) >= MaxPhotos {
		return nil, ValidationError{"photos": fmt.Sprintf("You can add up to %d photos. Remove one to add another.", MaxPhotos)}
	}
	if len(data) > MaxPhotoBytes {
		return nil, ValidationError{"photos": "That photo is over 15 MB. Please pick a smaller one."}
	}

	// Decoding a phone photo takes ~100 MB of RAM; cap how many run at once.
	select {
	case s.imaging <- struct{}{}:
		defer func() { <-s.imaging }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p, err := imaging.DecodePhoto(data, MinPhotoSide)
	switch {
	case errors.Is(err, imaging.ErrTooSmall):
		return nil, ValidationError{"photos": fmt.Sprintf("That photo is too small. Use one at least %d pixels on its short side.", MinPhotoSide)}
	case errors.Is(err, imaging.ErrUnsupported), errors.Is(err, imaging.ErrCorrupt):
		return nil, ValidationError{"photos": err.Error()}
	case err != nil:
		return nil, fmt.Errorf("add photo: decode: %w", err)
	}
	hash := p.PHash()
	w, h := p.Size()
	for _, m := range d.Photos() {
		if imaging.Distance(uint64(m.Phash), hash) <= dupDistance && sameShape(m.Width, m.Height, w, h) {
			return nil, ValidationError{"photos": "You've already added this photo."}
		}
	}

	mid := uuid.Must(uuid.NewV7())
	var stored []string
	cleanup := func() {
		for _, k := range stored {
			if err := s.media.Delete(context.WithoutCancel(ctx), k); err != nil {
				slog.WarnContext(ctx, "add photo: cleanup", "key", k, "err", err)
			}
		}
	}
	var total int
	var largest imaging.Rendition
	for _, sz := range PhotoSizes {
		r, err := p.Rendition(sz.Max, sz.Quality)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("add photo: %s: %w", sz.File, err)
		}
		key := MediaKey(mid, sz.File)
		if err := s.media.Put(ctx, key, r.JPEG); err != nil {
			cleanup()
			return nil, fmt.Errorf("add photo: store: %w", err)
		}
		stored = append(stored, key)
		total += len(r.JPEG)
		largest = r
	}

	pos := 0
	if ps := d.Photos(); len(ps) > 0 {
		pos = ps[len(ps)-1].Position + 1
	}
	m, err := s.db.ListingMedia.Create().SetID(mid).SetListingID(id).SetUploadedBy(a.UserID).
		SetPosition(pos).SetWidth(largest.Width).SetHeight(largest.Height).SetBytes(total).
		SetBlurhash(p.BlurHash()).SetPhash(int64(hash)).Save(ctx) //nolint:gosec // G115: the hash is 64 opaque bits
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("add photo: save: %w", err)
	}
	if err := s.refresh(ctx, d); err != nil {
		return nil, err
	}
	return m, nil
}

// sameShape reports whether two sizes have the same aspect ratio (±3%).
// The hash works on a squashed 32×32 thumbnail, so a portrait and a
// landscape photo can hash alike; real duplicates keep their shape.
func sameShape(w1, h1, w2, h2 int) bool {
	r1, r2 := float64(w1)/float64(h1), float64(w2)/float64(h2)
	return r1 > r2*0.97 && r1 < r2*1.03
}

// DeletePhoto removes a photo and its files.
func (s *Service) DeletePhoto(ctx context.Context, a Actor, id, mediaID uuid.UUID) error {
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return err
	}
	if !Editable(Status(d.L.Status)) {
		return ValidationError{"photos": "This listing can't be edited."}
	}
	photos := d.Photos()
	i := slices.IndexFunc(photos, func(m *ent.ListingMedia) bool { return m.ID == mediaID })
	if i < 0 {
		return ErrNotFound
	}
	rest := slices.Delete(photos, i, i+1)
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("delete photo: begin: %w", err)
	}
	if err := tx.ListingMedia.DeleteOneID(mediaID).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete photo: %w", err)
	}
	if err := renumber(ctx, tx, rest); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete photo: commit: %w", err)
	}
	for _, sz := range PhotoSizes {
		if err := s.media.Delete(ctx, MediaKey(mediaID, sz.File)); err != nil {
			slog.WarnContext(ctx, "delete photo: file", "media", mediaID, "err", err)
		}
	}
	return s.refresh(ctx, d)
}

// ReorderPhotos applies a new order to the photos (the video has no place
// in the gallery order). IDs the client doesn't know about (a
// photo added in another tab) keep their relative order at the end;
// unknown IDs are ignored.
func (s *Service) ReorderPhotos(ctx context.Context, a Actor, id uuid.UUID, order []uuid.UUID) error {
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return err
	}
	if !Editable(Status(d.L.Status)) {
		return ValidationError{"photos": "This listing can't be edited."}
	}
	photos := d.Photos()
	byID := map[uuid.UUID]*ent.ListingMedia{}
	for _, m := range photos {
		byID[m.ID] = m
	}
	var next []*ent.ListingMedia
	seen := map[uuid.UUID]bool{}
	for _, mid := range order {
		if m, ok := byID[mid]; ok && !seen[mid] {
			next = append(next, m)
			seen[mid] = true
		}
	}
	for _, m := range photos {
		if !seen[m.ID] {
			next = append(next, m)
		}
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("reorder photos: begin: %w", err)
	}
	if err := renumber(ctx, tx, next); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reorder photos: commit: %w", err)
	}
	return nil
}

// MovePhoto is the button version of drag-to-reorder: "cover", "left" or
// "right".
func (s *Service) MovePhoto(ctx context.Context, a Actor, id, mediaID uuid.UUID, how string) error {
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return err
	}
	photos := d.Photos()
	ids := make([]uuid.UUID, len(photos))
	for i, m := range photos {
		ids[i] = m.ID
	}
	i := slices.Index(ids, mediaID)
	if i < 0 {
		return ErrNotFound
	}
	switch {
	case how == "cover":
		ids = append([]uuid.UUID{mediaID}, slices.Delete(ids, i, i+1)...)
	case how == "left" && i > 0:
		ids[i-1], ids[i] = ids[i], ids[i-1]
	case how == "right" && i+1 < len(ids):
		ids[i+1], ids[i] = ids[i], ids[i+1]
	case how == "left", how == "right":
		return nil // already at the edge
	default:
		return ErrNotFound
	}
	return s.ReorderPhotos(ctx, a, id, ids)
}

// renumber writes positions 0..n-1 where they changed.
func renumber(ctx context.Context, tx *ent.Tx, ms []*ent.ListingMedia) error {
	for i, m := range ms {
		if m.Position == i {
			continue
		}
		if err := tx.ListingMedia.UpdateOneID(m.ID).SetPosition(i).Exec(ctx); err != nil {
			return fmt.Errorf("renumber photos: %w", err)
		}
	}
	return nil
}
