package listings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listingmedia"
	"rentmapgh/internal/platform/imaging"
	"rentmapgh/internal/platform/video"
)

// Walk-through video rules. One video per listing; it earns quality points
// but never blocks publishing.
const (
	MaxVideoBytes    = 250 << 20 // what a phone records in ~2 min at 1080p
	MaxVideoLength   = 2 * time.Minute
	MinVideoLength   = 5 * time.Second
	MinVideoSide     = 360
	MaxVideoChunk    = 8 << 20 // the uploader sends 2 MB pieces
	videoUploadTTL   = 24 * time.Hour
	videoProcessTime = 15 * time.Minute // per video, then it's marked failed
)

// VideoOutputs are the stored renditions. Both always exist, so a URL never
// 404s; small sources aren't upscaled. The poster is stored under the photo
// rendition names (w320.jpg …) of the same media ID.
var VideoOutputs = []struct {
	File           string
	ShortSide, CRF int
	MaxKbps        int
}{
	{"v480.mp4", 480, 28, 800},  // data saver, slow links (~6 MB/min)
	{"v720.mp4", 720, 26, 1800}, // default (~14 MB/min)
}

// IsVideoFile reports whether file is one of the video rendition names.
func IsVideoFile(file string) bool {
	for _, o := range VideoOutputs {
		if o.File == file {
			return true
		}
	}
	return false
}

var (
	ErrVideoOff     = errors.New("listings: video processing is not available")
	ErrUploadGone   = errors.New("listings: upload not found or expired")
	errNotProcessed = errors.New("listings: video source missing")
)

// OffsetError means a chunk didn't start where the upload stands; the
// client resumes from Have.
type OffsetError struct{ Have int64 }

func (e OffsetError) Error() string { return fmt.Sprintf("listings: upload is at byte %d", e.Have) }

// EnableVideo turns on walk-through videos. Uploads are assembled and
// transcoded under inbox (a local directory, so one web instance handles an
// upload from first chunk to finished video). Without ffmpeg the video
// section simply doesn't show.
func (s *Service) EnableVideo(t *video.Tool, inbox string) error {
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		return fmt.Errorf("listings: video inbox: %w", err)
	}
	s.video, s.inbox = t, inbox
	return nil
}

// VideoEnabled reports whether listers can add a video.
func (s *Service) VideoEnabled() bool { return s.video != nil }

// Video is the listing's walk-through video (in any state), or nil.
func (d *Item) Video() *ent.ListingMedia {
	for _, m := range d.M {
		if m.Kind == listingmedia.KindVideo {
			return m
		}
	}
	return nil
}

// HasReadyVideo reports whether renters can watch the video.
func (d *Item) HasReadyVideo() bool {
	v := d.Video()
	return v != nil && v.Status == listingmedia.StatusReady
}

// ── Upload ────────────────────────────────────────────────────────────────
// A phone video is tens of MB over a link that drops. The uploader sends it
// in pieces; after a failure it asks where the upload stands and carries on.
// The pieces are appended to inbox/{id}.part; {id}.json says whose it is.

type uploadMeta struct {
	ListingID uuid.UUID `json:"listing_id"`
	UserID    uuid.UUID `json:"user_id"`
	Size      int64     `json:"size"`
}

func (s *Service) inboxPath(id uuid.UUID, ext string) string {
	return filepath.Join(s.inbox, id.String()+ext)
}

// canAddVideo checks the listing is editable and has no video yet.
func (s *Service) canAddVideo(ctx context.Context, a Actor, id uuid.UUID) (*Item, error) {
	if s.video == nil {
		return nil, ErrVideoOff
	}
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if !Editable(Status(d.L.Status)) {
		return nil, ValidationError{"video": "This listing can't be edited."}
	}
	if d.Video() != nil {
		return nil, ValidationError{"video": "This listing already has a video. Delete it to add another."}
	}
	return d, nil
}

func sizeError(size int64) error {
	switch {
	case size <= 0:
		return ValidationError{"video": "Choose a video to upload."}
	case size > MaxVideoBytes:
		return ValidationError{"video": fmt.Sprintf("That video is over %d MB. Record a shorter one (under 2 minutes), or at 720p in your camera settings.", MaxVideoBytes>>20)}
	}
	return nil
}

// StartVideoUpload opens an upload of size bytes and returns its ID (which
// becomes the media ID).
func (s *Service) StartVideoUpload(ctx context.Context, a Actor, id uuid.UUID, size int64) (uuid.UUID, error) {
	if err := sizeError(size); err != nil {
		return uuid.Nil, err
	}
	if _, err := s.canAddVideo(ctx, a, id); err != nil {
		return uuid.Nil, err
	}
	uid := uuid.Must(uuid.NewV7())
	meta, _ := json.Marshal(uploadMeta{ListingID: id, UserID: a.UserID, Size: size})
	if err := os.WriteFile(s.inboxPath(uid, ".json"), meta, 0o600); err != nil {
		return uuid.Nil, fmt.Errorf("start upload: %w", err)
	}
	if err := os.WriteFile(s.inboxPath(uid, ".part"), nil, 0o600); err != nil {
		return uuid.Nil, fmt.Errorf("start upload: %w", err)
	}
	return uid, nil
}

// upload returns an open upload's metadata and current length, if it
// belongs to this lister and listing.
func (s *Service) upload(a Actor, id, uid uuid.UUID) (uploadMeta, int64, error) {
	var m uploadMeta
	raw, err := os.ReadFile(s.inboxPath(uid, ".json"))
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return m, 0, ErrUploadGone
	}
	if m.ListingID != id || m.UserID != a.UserID {
		return m, 0, ErrNotFound // someone else's: as if it didn't exist
	}
	st, err := os.Stat(s.inboxPath(uid, ".part"))
	if err != nil {
		return m, 0, ErrUploadGone
	}
	return m, st.Size(), nil
}

// VideoUploadOffset is how many bytes of an upload have arrived.
func (s *Service) VideoUploadOffset(a Actor, id, uid uuid.UUID) (int64, error) {
	if s.video == nil {
		return 0, ErrVideoOff
	}
	_, have, err := s.upload(a, id, uid)
	return have, err
}

// AppendVideoChunk adds the bytes in r at offset. When the last byte has
// arrived the video is checked and queued for processing, and done is true.
func (s *Service) AppendVideoChunk(ctx context.Context, a Actor, id, uid uuid.UUID, offset int64, r io.Reader) (have int64, done bool, err error) {
	if s.video == nil {
		return 0, false, ErrVideoOff
	}
	meta, have, err := s.upload(a, id, uid)
	if err != nil {
		return 0, false, err
	}
	if offset != have {
		return have, false, OffsetError{Have: have}
	}
	f, err := os.OpenFile(s.inboxPath(uid, ".part"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return have, false, ErrUploadGone
	}
	room := min(meta.Size-have, MaxVideoChunk)
	n, err := io.Copy(f, io.LimitReader(r, room+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if n > room {
		// More than was announced: the file is not what we agreed on.
		s.dropUpload(uid)
		return 0, false, ValidationError{"video": "The upload didn't match the file. Please try again."}
	}
	have += n
	if err != nil {
		// A dropped connection keeps what arrived; the client resumes.
		return have, false, fmt.Errorf("append chunk: %w", err)
	}
	if have < meta.Size {
		return have, false, nil
	}
	return have, true, s.finishVideo(ctx, a, id, uid)
}

// AddVideoFile is the no-script path: the whole file in one request,
// streamed to disk.
func (s *Service) AddVideoFile(ctx context.Context, a Actor, id uuid.UUID, r io.Reader) error {
	if _, err := s.canAddVideo(ctx, a, id); err != nil {
		return err
	}
	uid := uuid.Must(uuid.NewV7())
	part := s.inboxPath(uid, ".part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("add video: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxVideoBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = sizeError(n)
	}
	if err != nil {
		s.dropUpload(uid)
		return err
	}
	return s.finishVideo(ctx, a, id, uid)
}

// finishVideo checks a complete upload and queues it: the file becomes
// {id}.src and a "processing" media row appears. Nothing from the upload
// is stored as-is — it may carry the building's GPS position.
func (s *Service) finishVideo(ctx context.Context, a Actor, id, uid uuid.UUID) error {
	part := s.inboxPath(uid, ".part")
	fail := func(err error) error {
		s.dropUpload(uid)
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := s.video.Probe(pctx, part)
	switch {
	case errors.Is(err, video.ErrNotVideo):
		return fail(ValidationError{"video": err.Error()})
	case err != nil:
		return fail(fmt.Errorf("finish video: probe: %w", err))
	case info.Duration > MaxVideoLength+time.Second:
		return fail(ValidationError{"video": "That video is longer than 2 minutes. Keep the walk-through short: room, bathroom, kitchen, compound."})
	case info.Duration < MinVideoLength:
		return fail(ValidationError{"video": "That video is too short. Walk through the place for at least a few seconds."})
	case min(info.Width, info.Height) < MinVideoSide:
		return fail(ValidationError{"video": "That video is too small. Record at 480p or higher."})
	}
	// Re-check: another tab may have finished a video meanwhile.
	d, err := s.canAddVideo(ctx, a, id)
	if err != nil {
		return fail(err)
	}
	src := s.inboxPath(uid, ".src")
	if err := os.Rename(part, src); err != nil {
		return fail(fmt.Errorf("finish video: %w", err))
	}
	_ = os.Remove(s.inboxPath(uid, ".json"))
	ms := int(info.Duration.Milliseconds())
	if _, err := s.db.ListingMedia.Create().SetID(uid).SetListingID(id).SetUploadedBy(a.UserID).
		SetKind(listingmedia.KindVideo).SetStatus(listingmedia.StatusProcessing).
		SetWidth(info.Width).SetHeight(info.Height).SetBytes(0).SetDurationMs(max(ms, 1)).SetPhash(0).
		Save(ctx); err != nil {
		_ = os.Remove(src)
		return fmt.Errorf("finish video: save: %w", err)
	}
	s.WakeVideoWorker()
	return s.refresh(ctx, d)
}

func (s *Service) dropUpload(uid uuid.UUID) {
	for _, ext := range []string{".part", ".json", ".src"} {
		if err := os.Remove(s.inboxPath(uid, ext)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("listings: drop upload", "upload", uid, "err", err)
		}
	}
}

// DeleteVideo removes the listing's video, whatever its state.
func (s *Service) DeleteVideo(ctx context.Context, a Actor, id uuid.UUID) error {
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return err
	}
	if !Editable(Status(d.L.Status)) {
		return ValidationError{"video": "This listing can't be edited."}
	}
	v := d.Video()
	if v == nil {
		return nil
	}
	if err := s.db.ListingMedia.DeleteOneID(v.ID).Exec(ctx); err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("delete video: %w", err)
	}
	s.deleteVideoFiles(ctx, v.ID)
	return s.refresh(ctx, d)
}

func (s *Service) deleteVideoFiles(ctx context.Context, mid uuid.UUID) {
	if s.inbox != "" {
		s.dropUpload(mid)
	}
	for _, f := range videoFiles() {
		if err := s.media.Delete(context.WithoutCancel(ctx), MediaKey(mid, f)); err != nil {
			slog.WarnContext(ctx, "delete video: file", "media", mid, "file", f, "err", err)
		}
	}
}

func videoFiles() []string {
	var out []string
	for _, o := range VideoOutputs {
		out = append(out, o.File)
	}
	for _, sz := range PhotoSizes {
		out = append(out, sz.File)
	}
	return out
}

// ── Processing ────────────────────────────────────────────────────────────

// WakeVideoWorker nudges RunVideoWorker after an upload.
func (s *Service) WakeVideoWorker() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// RunVideoWorker transcodes queued videos one at a time until ctx ends,
// and tidies abandoned uploads. (Moves to River with the job queue.)
func (s *Service) RunVideoWorker(ctx context.Context) {
	if s.video == nil {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		for {
			n, err := s.ProcessVideos(ctx)
			if err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "jobs: process videos", "err", err)
			}
			if n == 0 || err != nil {
				break
			}
		}
		s.tidyInbox(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-t.C:
		}
	}
}

// ProcessVideos transcodes the oldest queued video whose upload is on this
// machine, and returns how many it handled (0 or 1).
func (s *Service) ProcessVideos(ctx context.Context) (int, error) {
	queued, err := s.db.ListingMedia.Query().
		Where(listingmedia.KindEQ(listingmedia.KindVideo), listingmedia.StatusEQ(listingmedia.StatusProcessing)).
		Order(ent.Asc(listingmedia.FieldCreatedAt)).Limit(50).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("process videos: %w", err)
	}
	for _, m := range queued {
		src := s.inboxPath(m.ID, ".src")
		if _, err := os.Stat(src); err != nil {
			continue // another instance's upload, or lost (tidyInbox fails it)
		}
		err := s.processVideo(ctx, m, src)
		if ctx.Err() != nil {
			return 0, ctx.Err() // shutting down: the source stays for next boot
		}
		if err != nil {
			slog.ErrorContext(ctx, "jobs: video failed", "media", m.ID, "err", err)
			s.deleteVideoFiles(ctx, m.ID)
			if uerr := s.db.ListingMedia.UpdateOneID(m.ID).SetStatus(listingmedia.StatusFailed).Exec(ctx); uerr != nil && !ent.IsNotFound(uerr) {
				return 1, fmt.Errorf("process videos: mark failed: %w", uerr)
			}
		}
		return 1, nil
	}
	return 0, nil
}

func (s *Service) processVideo(ctx context.Context, m *ent.ListingMedia, src string) error {
	ctx, cancel := context.WithTimeout(ctx, videoProcessTime)
	defer cancel()
	start := time.Now()
	tmp, err := os.MkdirTemp(s.inbox, "work-")
	if err != nil {
		return fmt.Errorf("process video: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	info, err := s.video.Probe(ctx, src)
	if err != nil {
		return fmt.Errorf("process video: probe: %w", err)
	}
	var outs []video.Output
	for _, o := range VideoOutputs {
		outs = append(outs, video.Output{Path: filepath.Join(tmp, o.File), ShortSide: o.ShortSide, CRF: o.CRF, MaxKbps: o.MaxKbps})
	}
	if err := s.video.Transcode(ctx, src, MaxVideoLength, info.HasAudio, outs...); err != nil {
		return err
	}
	main := outs[len(outs)-1].Path
	out, err := s.video.Probe(ctx, main)
	if err != nil {
		return fmt.Errorf("process video: probe output: %w", err)
	}
	frame, err := s.video.Frame(ctx, main, min(time.Second, out.Duration/3))
	if err != nil {
		return err
	}
	poster, err := imaging.DecodePhoto(frame, 1)
	if err != nil {
		return fmt.Errorf("process video: poster: %w", err)
	}

	total := 0
	for _, o := range outs {
		data, err := os.ReadFile(o.Path)
		if err != nil {
			return fmt.Errorf("process video: %w", err)
		}
		if err := s.media.Put(ctx, MediaKey(m.ID, filepath.Base(o.Path)), data); err != nil {
			return fmt.Errorf("process video: store: %w", err)
		}
		total += len(data)
	}
	for _, sz := range PhotoSizes {
		r, err := poster.Rendition(sz.Max, sz.Quality)
		if err != nil {
			return fmt.Errorf("process video: poster %s: %w", sz.File, err)
		}
		if err := s.media.Put(ctx, MediaKey(m.ID, sz.File), r.JPEG); err != nil {
			return fmt.Errorf("process video: store: %w", err)
		}
		total += len(r.JPEG)
	}

	err = s.db.ListingMedia.UpdateOneID(m.ID).SetStatus(listingmedia.StatusReady).
		SetWidth(out.Width).SetHeight(out.Height).SetBytes(total).
		SetBlurhash(poster.BlurHash()).SetPhash(int64(poster.PHash())). //nolint:gosec // G115: the hash is 64 opaque bits
		Exec(ctx)
	if ent.IsNotFound(err) {
		// Deleted by the lister while we worked.
		s.deleteVideoFiles(ctx, m.ID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("process video: save: %w", err)
	}
	_ = os.Remove(src)
	slog.InfoContext(ctx, "jobs: video ready", "media", m.ID, "took", time.Since(start).Round(time.Millisecond), "bytes", total)
	return s.rescore(ctx, m.ListingID)
}

// rescore refreshes a listing's derived fields outside a lister request.
func (s *Service) rescore(ctx context.Context, id uuid.UUID) error {
	l, err := s.db.Listing.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("rescore: %w", err)
	}
	return s.refresh(ctx, &Item{L: l})
}

// tidyInbox deletes uploads abandoned for a day, and fails videos whose
// source vanished (e.g. the instance's disk was replaced).
func (s *Service) tidyInbox(ctx context.Context) {
	cutoff := s.now().Add(-videoUploadTTL)
	entries, err := os.ReadDir(s.inbox)
	if err != nil {
		slog.WarnContext(ctx, "jobs: video inbox", "err", err)
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		switch ext := filepath.Ext(e.Name()); {
		case e.IsDir():
			_ = os.RemoveAll(filepath.Join(s.inbox, e.Name())) // a crashed transcode
		case ext == ".part" || ext == ".json":
			_ = os.Remove(filepath.Join(s.inbox, e.Name()))
		}
	}
	stale, err := s.db.ListingMedia.Query().
		Where(listingmedia.KindEQ(listingmedia.KindVideo), listingmedia.StatusEQ(listingmedia.StatusProcessing),
			listingmedia.CreatedAtLT(cutoff)).All(ctx)
	if err != nil {
		return
	}
	for _, m := range stale {
		if _, err := os.Stat(s.inboxPath(m.ID, ".src")); err == nil {
			continue
		}
		slog.WarnContext(ctx, "jobs: video source lost", "media", m.ID, "err", errNotProcessed)
		_ = s.db.ListingMedia.UpdateOneID(m.ID).SetStatus(listingmedia.StatusFailed).Exec(ctx)
	}
}
