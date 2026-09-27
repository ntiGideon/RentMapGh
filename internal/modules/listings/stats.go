package listings

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/listingstat"
)

// Listing stats (Phase 5 dashboard): daily views, saves and contacts per
// listing. Counting never fails the request that triggers it.

// StatKind is a counter.
type StatKind string

const (
	StatView    StatKind = "views"
	StatSave    StatKind = "saves"
	StatContact StatKind = "contacts"
)

func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Bump adds one to a listing's counter for today.
func (s *Service) Bump(ctx context.Context, id uuid.UUID, k StatKind) {
	col := map[StatKind]string{StatView: "views", StatSave: "saves", StatContact: "contacts"}[k]
	if col == "" {
		return
	}
	// One statement: insert today's row with 1, or add 1 to it.
	stmt := fmt.Sprintf(`INSERT INTO listing_stats (id, listing_id, day, views, saves, contacts)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (listing_id, day) DO UPDATE SET %[1]s = listing_stats.%[1]s + 1`, col)
	v, sv, c := 0, 0, 0
	switch k {
	case StatView:
		v = 1
	case StatSave:
		sv = 1
	case StatContact:
		c = 1
	}
	if _, err := s.db.ExecContext(ctx, stmt, uuid.Must(uuid.NewV7()), id, day(s.now()), v, sv, c); err != nil {
		slog.WarnContext(ctx, "stats: bump", "kind", k, "err", err)
	}
	if k == StatView {
		if err := s.db.Listing.UpdateOneID(id).AddViewsCount(1).Exec(ctx); err != nil {
			slog.WarnContext(ctx, "stats: views_count", "err", err)
		}
	}
}

// ── Views, deduplicated ──────────────────────────────────────────────────

// botUA matches crawlers and link previewers (WhatsApp, Facebook, X…),
// which fetch pages without a person behind them.
var botUA = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|preview|whatsapp|facebookexternalhit|telegram|curl|wget|python-|go-http|headless`)

// viewSeen remembers who viewed what today, so reloads count once.
type viewSeen struct {
	mu   sync.Mutex
	day  time.Time
	seen map[[16]byte]struct{}
}

var views = &viewSeen{seen: map[[16]byte]struct{}{}}

const maxSeen = 200_000 // ~6 MB; past this, views just aren't deduplicated

func (v *viewSeen) first(today time.Time, key string) bool {
	h := sha256.Sum256([]byte(key))
	var k [16]byte
	copy(k[:], h[:16])
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.day.Equal(today) {
		v.day, v.seen = today, map[[16]byte]struct{}{}
	}
	if _, ok := v.seen[k]; ok {
		return false
	}
	if len(v.seen) < maxSeen {
		v.seen[k] = struct{}{}
	}
	return true
}

// CountView records a public page view (not the lister's own, not bots,
// once per visitor per day).
func (s *Service) CountView(ctx context.Context, l *ent.Listing, viewer uuid.UUID, ip, ua string) {
	if l.Status != listing.StatusActive || l.ListerID == viewer || ua == "" || botUA.MatchString(ua) {
		return
	}
	who := ip + "|" + ua
	if viewer != uuid.Nil {
		who = viewer.String()
	}
	if views.first(day(s.now()), who+"|"+l.ID.String()) {
		s.Bump(ctx, l.ID, StatView)
	}
}

// ── Reading ──────────────────────────────────────────────────────────────

// Counts is a listing's (or all of a lister's) totals over a period.
type Counts struct{ Views, Saves, Contacts int }

// ContactRate is contacts per 100 views.
func (c Counts) ContactRate() float64 {
	if c.Views == 0 {
		return 0
	}
	return float64(c.Contacts) * 100 / float64(c.Views)
}

// CountsFor sums the last `days` days for each listing.
func (s *Service) CountsFor(ctx context.Context, ids []uuid.UUID, days int) (map[uuid.UUID]Counts, error) {
	out := map[uuid.UUID]Counts{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.ListingStat.Query().Where(listingstat.ListingIDIn(ids...),
		listingstat.DayGTE(day(s.now()).AddDate(0, 0, -(days-1)))).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stats: counts: %w", err)
	}
	for _, r := range rows {
		c := out[r.ListingID]
		c.Views += r.Views
		c.Saves += r.Saves
		c.Contacts += r.Contacts
		out[r.ListingID] = c
	}
	return out, nil
}

// DayStat is one day of a series.
type DayStat struct {
	Day                    time.Time
	Views, Saves, Contacts int
}

// Series is a listing's last `days` days, oldest first, zero-filled.
func (s *Service) Series(ctx context.Context, id uuid.UUID, days int) ([]DayStat, error) {
	from := day(s.now()).AddDate(0, 0, -(days - 1))
	rows, err := s.db.ListingStat.Query().Where(listingstat.ListingID(id), listingstat.DayGTE(from)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stats: series: %w", err)
	}
	byDay := map[time.Time]*ent.ListingStat{}
	for _, r := range rows {
		byDay[day(r.Day)] = r
	}
	out := make([]DayStat, days)
	for i := range out {
		d := from.AddDate(0, 0, i)
		out[i].Day = d
		if r := byDay[d]; r != nil {
			out[i].Views, out[i].Saves, out[i].Contacts = r.Views, r.Saves, r.Contacts
		}
	}
	return out, nil
}

// median of durations (0 when empty).
func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}
