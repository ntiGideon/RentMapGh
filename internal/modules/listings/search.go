package listings

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/listingmedia"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/platform/money"
)

// Search (ProjectRequirement §6.2–6.3). Every location condition reads
// properties.approx_geog: filtering on the exact point — even by bounding
// box — would let repeated shrinking queries find the building (§6.1).

// PageSize is how many results the list shows per page.
const PageSize = 24

// MaxMarkers caps the map's GeoJSON; MapLibre clusters this many easily.
const MaxMarkers = 2000

// BBox is a map viewport in degrees.
type BBox struct{ MinLng, MinLat, MaxLng, MaxLat float64 }

// DefaultBBox frames the KNUST student belt.
var DefaultBBox = BBox{MinLng: -1.6050, MinLat: 6.6480, MaxLng: -1.5300, MaxLat: 6.7000}

func (b BBox) Centre() geo.Point { return geo.Point{Lat: (b.MinLat + b.MaxLat) / 2, Lng: (b.MinLng + b.MaxLng) / 2} }

func (b BBox) String() string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 5, 64) }
	return f(b.MinLng) + "," + f(b.MinLat) + "," + f(b.MaxLng) + "," + f(b.MaxLat)
}

// Sorts, in the order the menu shows them.
var Sorts = []Option{
	{"recommended", "Recommended"},
	{"newest", "Newest"},
	{"price_asc", "Price: low to high"},
	{"price_desc", "Price: high to low"},
	{"nearest", "Nearest to map centre"},
	{"confirmed", "Recently confirmed"},
}

// TypeGroups are the unit-type chips: one tap selects a whole family.
var TypeGroups = []struct {
	Key, Label string
	Types      []string
}{
	{"rooms", "Rooms", []string{"single_room", "single_room_sc", "chamber_hall", "chamber_hall_sc", "compound_room", "boys_quarters"}},
	{"hostels", "Hostels", []string{"hostel_1in1", "hostel_2in1", "hostel_4in1"}},
	{"apartments", "Apartments & houses", []string{"apartment_1bed", "apartment_2bed", "apartment_3bed", "apartment_4bed", "detached_house", "semi_detached", "townhouse"}},
	{"commercial", "Shops & offices", []string{"shop", "store", "office", "event_space"}},
}

// Feature filters that map to amenities; "security" matches any of several.
var FeatureFilters = []struct {
	Key, Label string
	AnyOf      []string
}{
	{"security", "Security", []string{"gated", "watchman", "cctv"}},
	{"parking", "Parking", []string{"parking"}},
	{"ac", "Air conditioning", []string{"ac"}},
	{"wifi", "Wi-Fi", []string{"wifi"}},
	{"pets", "Pets allowed", []string{"pets_allowed"}},
	{"backup", "Backup power", []string{"backup_power"}},
}

// Filter is a search, round-tripped through the URL (shareable, back-button
// safe). Zero values mean "any".
type Filter struct {
	BBox      BBox
	HasBBox   bool
	MinPrice  int      // GHS per month (monthly equivalent)
	MaxPrice  int      // GHS per month
	Groups    []string // TypeGroups keys
	Beds      int      // at least
	Baths     int      // at least
	SC        bool     // self-contained
	Furnished bool     // semi or fully
	OwnMeter  bool
	Water     string   // WaterOptions value
	Kitchen   string   // "private"
	Features  []string // FeatureFilters keys
	Verified  bool     // lister's ID checked
	Owner     bool     // listed by the owner, not an agent
	From      string   // available by, YYYY-MM-DD
	Sort      string
	Page      int // 1-based
}

// ParseFilter reads a filter from query parameters, ignoring anything it
// doesn't recognise (links get pasted and edited by hand).
func ParseFilter(q url.Values) Filter {
	f := Filter{Sort: "recommended", Page: 1}
	if b, ok := parseBBox(q.Get("bbox")); ok {
		f.BBox, f.HasBBox = b, true
	} else {
		f.BBox = DefaultBBox
	}
	atoi := func(k string, lo, hi int) int {
		n, err := strconv.Atoi(strings.ReplaceAll(q.Get(k), ",", ""))
		if err != nil || n < lo {
			return 0
		}
		return min(n, hi)
	}
	f.MinPrice, f.MaxPrice = atoi("min", 0, 1_000_000), atoi("max", 0, 1_000_000)
	if f.MaxPrice > 0 && f.MinPrice > f.MaxPrice {
		f.MinPrice, f.MaxPrice = f.MaxPrice, f.MinPrice
	}
	for _, g := range q["type"] {
		if slices.ContainsFunc(TypeGroups, func(t struct {
			Key, Label string
			Types      []string
		}) bool {
			return t.Key == g
		}) && !slices.Contains(f.Groups, g) {
			f.Groups = append(f.Groups, g)
		}
	}
	f.Beds, f.Baths = atoi("beds", 1, 6), atoi("baths", 1, 6)
	f.SC, f.Furnished, f.OwnMeter = q.Get("sc") == "1", q.Get("furnished") == "1", q.Get("meter") == "1"
	f.Verified, f.Owner = q.Get("verified") == "1", q.Get("owner") == "1"
	if w := q.Get("water"); optionValid(WaterOptions, w) && w != "none" {
		f.Water = w
	}
	if q.Get("kitchen") == "private" {
		f.Kitchen = "private"
	}
	for _, k := range q["feature"] {
		for _, ff := range FeatureFilters {
			if ff.Key == k && !slices.Contains(f.Features, k) {
				f.Features = append(f.Features, k)
			}
		}
	}
	if d, err := time.Parse("2006-01-02", q.Get("from")); err == nil {
		f.From = d.Format("2006-01-02")
	}
	if s := q.Get("sort"); optionValid(Sorts, s) {
		f.Sort = s
	}
	f.Page = max(1, atoi("page", 1, 100))
	return f
}

func parseBBox(s string) (BBox, bool) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return BBox{}, false
	}
	var v [4]float64
	for i, p := range parts {
		x, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || math.IsNaN(x) || math.IsInf(x, 0) {
			return BBox{}, false
		}
		v[i] = x
	}
	b := BBox{MinLng: v[0], MinLat: v[1], MaxLng: v[2], MaxLat: v[3]}
	if b.MinLng >= b.MaxLng || b.MinLat >= b.MaxLat || b.MinLat < -90 || b.MaxLat > 90 || b.MinLng < -180 || b.MaxLng > 180 {
		return BBox{}, false
	}
	// A whole-country view is fine; a whole-world one is a scrape.
	if b.MaxLng-b.MinLng > 8 || b.MaxLat-b.MinLat > 8 {
		return BBox{}, false
	}
	return b, true
}

// Query encodes the filter back to URL parameters (page omitted when 1).
func (f Filter) Query() url.Values {
	q := url.Values{}
	if f.HasBBox {
		q.Set("bbox", f.BBox.String())
	}
	set := func(k string, n int) {
		if n > 0 {
			q.Set(k, strconv.Itoa(n))
		}
	}
	flag := func(k string, b bool) {
		if b {
			q.Set(k, "1")
		}
	}
	set("min", f.MinPrice)
	set("max", f.MaxPrice)
	for _, g := range f.Groups {
		q.Add("type", g)
	}
	set("beds", f.Beds)
	set("baths", f.Baths)
	flag("sc", f.SC)
	flag("furnished", f.Furnished)
	flag("meter", f.OwnMeter)
	if f.Water != "" {
		q.Set("water", f.Water)
	}
	if f.Kitchen != "" {
		q.Set("kitchen", f.Kitchen)
	}
	for _, k := range f.Features {
		q.Add("feature", k)
	}
	flag("verified", f.Verified)
	flag("owner", f.Owner)
	if f.From != "" {
		q.Set("from", f.From)
	}
	if f.Sort != "recommended" && f.Sort != "" {
		q.Set("sort", f.Sort)
	}
	if f.Page > 1 {
		q.Set("page", strconv.Itoa(f.Page))
	}
	return q
}

// ActiveCount is how many filters (not the map view or sort) are set.
func (f Filter) ActiveCount() int {
	n := len(f.Groups) + len(f.Features)
	for _, b := range []bool{f.MinPrice > 0 || f.MaxPrice > 0, f.Beds > 0, f.Baths > 0, f.SC, f.Furnished, f.OwnMeter,
		f.Water != "", f.Kitchen != "", f.Verified, f.Owner, f.From != ""} {
		if b {
			n++
		}
	}
	return n
}

// where builds the shared WHERE clause and its arguments.
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	conds = append(conds, "l.status = 'active'", "p.approx_geog IS NOT NULL",
		fmt.Sprintf("p.approx_geog && ST_MakeEnvelope(%s, %s, %s, %s, 4326)::geography",
			arg(f.BBox.MinLng), arg(f.BBox.MinLat), arg(f.BBox.MaxLng), arg(f.BBox.MaxLat)))
	if f.MinPrice > 0 {
		conds = append(conds, "t.monthly_equivalent >= "+arg(int64(f.MinPrice)*100))
	}
	if f.MaxPrice > 0 {
		conds = append(conds, "t.monthly_equivalent <= "+arg(int64(f.MaxPrice)*100))
	}
	if len(f.Groups) > 0 {
		var types []string
		for _, g := range TypeGroups {
			if slices.Contains(f.Groups, g.Key) {
				types = append(types, g.Types...)
			}
		}
		conds = append(conds, "u.unit_type = ANY("+arg(types)+")")
	}
	if f.Beds > 0 {
		conds = append(conds, "COALESCE(u.bedrooms, 1) >= "+arg(f.Beds))
	}
	if f.Baths > 0 {
		conds = append(conds, "COALESCE(u.bathrooms, 0) >= "+arg(f.Baths))
	}
	if f.SC {
		conds = append(conds, "u.self_contained IS TRUE")
	}
	if f.Furnished {
		conds = append(conds, "u.furnished IN ('semi', 'full')")
	}
	if f.OwnMeter {
		conds = append(conds, "u.meter_type = 'prepaid_own'")
	}
	if f.Water != "" {
		conds = append(conds, "u.water_source = "+arg(f.Water))
	}
	if f.Kitchen != "" {
		conds = append(conds, "u.kitchen = "+arg(f.Kitchen))
	}
	for _, k := range f.Features {
		for _, ff := range FeatureFilters {
			if ff.Key != k {
				continue
			}
			var alts []string
			for _, a := range ff.AnyOf {
				j, _ := json.Marshal([]string{a})
				alts = append(alts, "u.amenities @> "+arg(string(j))+"::jsonb")
			}
			conds = append(conds, "("+strings.Join(alts, " OR ")+")")
		}
	}
	if f.Verified {
		conds = append(conds, "EXISTS (SELECT 1 FROM users usr WHERE usr.id = l.lister_id AND usr.identity_verified_at IS NOT NULL)")
	}
	if f.Owner {
		conds = append(conds, "l.lister_kind = 'owner'")
	}
	if f.From != "" {
		conds = append(conds, "(l.available_from IS NULL OR l.available_from <= "+arg(f.From)+"::date)")
	}
	return strings.Join(conds, "\n  AND "), args
}

const searchFrom = `FROM listings l
JOIN units u ON u.id = l.unit_id
JOIN properties p ON p.id = u.property_id
JOIN listing_terms t ON t.listing_id = l.id`

func (f Filter) orderBy(args *[]any) string {
	c := f.BBox.Centre()
	switch f.Sort {
	case "newest":
		return "l.published_at DESC NULLS LAST, l.id DESC"
	case "price_asc":
		return "t.monthly_equivalent ASC NULLS LAST, l.id"
	case "price_desc":
		return "t.monthly_equivalent DESC NULLS LAST, l.id"
	case "confirmed":
		return "l.last_confirmed_at DESC NULLS LAST, l.id DESC"
	case "nearest":
		*args = append(*args, c.Lng, c.Lat)
		n := len(*args)
		return fmt.Sprintf("p.approx_geog <-> ST_SetSRID(ST_MakePoint($%d, $%d), 4326)::geography, l.id", n-1, n)
	}
	// Recommended: promoted first, then quality, then freshness.
	return "(l.promoted_until > now()) DESC NULLS LAST, l.quality_score DESC, l.last_confirmed_at DESC NULLS LAST, l.id DESC"
}

// Results is one page of a search.
type Results struct {
	Items []*Item
	Total int
}

// Search runs a filter and loads one page of listings, in order.
func (s *Service) Search(ctx context.Context, f Filter) (Results, error) {
	where, args := f.where()
	order := f.orderBy(&args)
	args = append(args, PageSize, (f.Page-1)*PageSize)
	q := fmt.Sprintf("SELECT l.id, count(*) OVER ()\n%s\nWHERE %s\nORDER BY %s\nLIMIT $%d OFFSET $%d",
		searchFrom, where, order, len(args)-1, len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Results{}, fmt.Errorf("search: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []uuid.UUID
	var res Results
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id, &res.Total); err != nil {
			return Results{}, fmt.Errorf("search: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return Results{}, fmt.Errorf("search: rows: %w", err)
	}
	if len(ids) == 0 && f.Page > 1 {
		// Past the end: report the real total so the view can say so.
		if n, err := s.Count(ctx, f); err == nil {
			res.Total = n
		}
	}
	res.Items, err = s.loadPublic(ctx, ids)
	return res, err
}

// Count is the number of matches (for "Show 42 places" on the filter sheet).
func (s *Service) Count(ctx context.Context, f Filter) (int, error) {
	where, args := f.where()
	var n int
	row := s.db.QueryContext
	rows, err := row(ctx, "SELECT count(*)\n"+searchFrom+"\nWHERE "+where, args...)
	if err != nil {
		return 0, fmt.Errorf("search count: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// Marker is one listing on the map. Coordinates are the approximate point.
type Marker struct {
	ID       uuid.UUID
	Lat, Lng float64
	Label    string // "₵650", "₵4.5k/yr"
}

// Markers returns every match in the viewport (up to MaxMarkers).
func (s *Service) Markers(ctx context.Context, f Filter) ([]Marker, error) {
	where, args := f.where()
	args = append(args, MaxMarkers)
	q := fmt.Sprintf("SELECT l.id, p.approx_lat, p.approx_lng, t.rent, t.rent_period\n%s\nWHERE %s\nORDER BY l.quality_score DESC\nLIMIT $%d",
		searchFrom, where, len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("markers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Marker
	for rows.Next() {
		var m Marker
		var rent *int64
		var period string
		if err := rows.Scan(&m.ID, &m.Lat, &m.Lng, &rent, &period); err != nil {
			return nil, fmt.Errorf("markers: scan: %w", err)
		}
		if rent != nil {
			m.Label = PriceLabel(money.Pesewas(*rent), period)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PriceLabel is the short price on a map pill: ₵650, ₵1.2k, ₵4.5k/yr.
func PriceLabel(p money.Pesewas, period string) string {
	cedis := float64(p) / 100
	var s string
	switch {
	case cedis >= 10_000:
		s = strconv.FormatFloat(math.Round(cedis/1000), 'f', 0, 64) + "k"
	case cedis >= 1000:
		s = strings.TrimSuffix(strconv.FormatFloat(math.Round(cedis/100)/10, 'f', 1, 64), ".0") + "k"
	default:
		s = strconv.FormatFloat(math.Round(cedis), 'f', 0, 64)
	}
	switch period {
	case "semester":
		s += "/sem"
	case "academic_year", "year":
		s += "/yr"
	}
	return "₵" + s
}

// loadPublic loads live listings by ID, keeping the given order, with only
// ready media.
func (s *Service) loadPublic(ctx context.Context, ids []uuid.UUID) ([]*Item, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ls, err := s.db.Listing.Query().Where(listing.IDIn(ids...)).
		WithTerms().WithUnit(func(q *ent.UnitQuery) { q.WithProperty() }).
		WithMedia(func(q *ent.ListingMediaQuery) {
			withMedia(q)
			q.Where(listingmedia.StatusEQ(listingmedia.StatusReady))
		}).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("search: load: %w", err)
	}
	byID := make(map[uuid.UUID]*ent.Listing, len(ls))
	for _, l := range ls {
		byID[l.ID] = l
	}
	out := make([]*Item, 0, len(ids))
	for _, id := range ids {
		if l, ok := byID[id]; ok {
			if d := draftOf(l); d.U != nil && d.P != nil && d.T != nil {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

// Similar returns up to n live listings near d of the same type family,
// nearest first.
func (s *Service) Similar(ctx context.Context, d *Item, n int) ([]*Item, error) {
	if d.P.ApproxLat == nil || d.P.ApproxLng == nil {
		return nil, nil
	}
	c := geo.Point{Lat: *d.P.ApproxLat, Lng: *d.P.ApproxLng}
	f := Filter{Sort: "nearest", Page: 1, HasBBox: true,
		BBox: BBox{MinLng: c.Lng - 0.03, MinLat: c.Lat - 0.03, MaxLng: c.Lng + 0.03, MaxLat: c.Lat + 0.03}}
	for _, g := range TypeGroups {
		if slices.Contains(g.Types, d.U.UnitType) {
			f.Groups = []string{g.Key}
		}
	}
	res, err := s.Search(ctx, f)
	if err != nil {
		return nil, err
	}
	var out []*Item
	for _, it := range res.Items {
		if it.L.ID != d.L.ID && it.P.ID != d.P.ID && len(out) < n {
			out = append(out, it)
		}
	}
	return out, nil
}
