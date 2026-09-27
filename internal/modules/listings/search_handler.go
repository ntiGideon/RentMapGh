package listings

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/server/render"
	c "rentmapgh/internal/views/components"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

// Search is the map + list page. htmx requests get just the results pane
// (render.Page), so the same URL works opened directly or shared.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	f := ParseFilter(r.URL.Query())
	res, err := h.svc.Search(r.Context(), f)
	if err != nil {
		slog.ErrorContext(r.Context(), "search", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	v := h.searchView(r.Context(), f, res, h.savedFor(w, r))
	m := layouts.Meta{
		Title:       "Rooms, hostels and apartments near KNUST",
		Description: "Find a room, hostel or apartment in Kumasi on a map, with the full move-in cost of every place. No agent runaround.",
		URL:         h.baseURL + "/search",
		NoIndex:     r.URL.RawQuery != "", // filtered views are for sharing, not for search engines
		Styles:      []string{"vendor/maplibre-6.11.2/maplibre-gl.css"},
		Modules:     []string{"js/search.js", "js/compare.js"},
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	render.Page(w, r, http.StatusOK, pages.Search(m, v), pages.SearchResults(v))
}

// Markers is the map's GeoJSON for the same filter (approximate points).
func (h *Handler) Markers(w http.ResponseWriter, r *http.Request) {
	ms, err := h.svc.Markers(r.Context(), ParseFilter(r.URL.Query()))
	if err != nil {
		slog.ErrorContext(r.Context(), "search: markers", "err", err)
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	type props struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	type feature struct {
		Type     string `json:"type"`
		Geometry struct {
			Type        string     `json:"type"`
			Coordinates [2]float64 `json:"coordinates"`
		} `json:"geometry"`
		Properties props `json:"properties"`
	}
	out := struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}{Type: "FeatureCollection", Features: make([]feature, 0, len(ms))}
	for _, m := range ms {
		ft := feature{Type: "Feature", Properties: props{ID: m.ID.String(), Label: m.Label}}
		ft.Geometry.Type = "Point"
		ft.Geometry.Coordinates = [2]float64{round5(m.Lng), round5(m.Lat)}
		out.Features = append(out.Features, ft)
	}
	w.Header().Set("Content-Type", "application/geo+json")
	w.Header().Set("Cache-Control", "public, max-age=30")
	_ = json.NewEncoder(w).Encode(out)
}

func round5(v float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 5, 64), 64)
	return f
}

// Preview is the card for a tapped marker (live listings only).
func (h *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	items, err := h.svc.loadPublic(r.Context(), []uuid.UUID{id})
	if err != nil || len(items) == 0 || items[0].L.Status != listing.StatusActive {
		http.NotFound(w, r)
		return
	}
	cards := h.cards(r.Context(), items, Filter{}.Ref(), h.savedFor(w, r))
	w.Header().Set("Cache-Control", "public, max-age=30")
	render.Component(w, r, http.StatusOK, pages.ListingPreview(cards[0]))
}

func (h *Handler) searchView(ctx context.Context, f Filter, res Results, saved []uuid.UUID) pages.SearchView {
	v := pages.SearchView{
		Total: res.Total, Page: f.Page, Pages: (res.Total + PageSize - 1) / PageSize,
		Results: h.cards(ctx, res.Items, f.Ref(), saved), BBox: f.BBox.String(), DefaultBBox: DefaultBBox.String(), Sort: f.Sort, Active: f.ActiveCount(),
		SC: f.SC, Furnished: f.Furnished, OwnMeter: f.OwnMeter, Kitchen: f.Kitchen != "", Verified: f.Verified, Owner: f.Owner,
		Water: f.Water, From: f.From,
	}
	if !f.HasBBox {
		v.BBox = ""
	}
	for _, s := range Sorts {
		v.Sorts = append(v.Sorts, c.Option{Value: s.Value, Label: s.Label})
	}
	for _, o := range WaterOptions {
		if o.Value != "none" {
			v.WaterOpts = append(v.WaterOpts, c.Option{Value: o.Value, Label: o.Label})
		}
	}
	for _, g := range TypeGroups {
		v.Groups = append(v.Groups, pages.Chip{Key: g.Key, Label: g.Label, On: slices.Contains(f.Groups, g.Key)})
	}
	for _, ff := range FeatureFilters {
		v.Features = append(v.Features, pages.Chip{Key: ff.Key, Label: ff.Label, On: slices.Contains(f.Features, ff.Key)})
	}
	if f.MinPrice > 0 {
		v.Min = strconv.Itoa(f.MinPrice)
	}
	if f.MaxPrice > 0 {
		v.Max = strconv.Itoa(f.MaxPrice)
	}
	if f.Beds > 0 {
		v.Beds = strconv.Itoa(f.Beds)
	}
	if f.Baths > 0 {
		v.Baths = strconv.Itoa(f.Baths)
	}
	page := func(n int) string {
		g := f
		g.Page = n
		return "/search?" + g.Query().Encode()
	}
	if f.Page > 1 {
		v.PrevURL = page(f.Page - 1)
	}
	if f.Page < v.Pages {
		v.NextURL = page(f.Page + 1)
	}
	wider := f
	wider.Page, wider.HasBBox = 1, true
	cx, cy := f.BBox.Centre().Lng, f.BBox.Centre().Lat
	dx, dy := f.BBox.MaxLng-f.BBox.MinLng, f.BBox.MaxLat-f.BBox.MinLat
	wider.BBox = BBox{MinLng: cx - dx, MinLat: cy - dy, MaxLng: cx + dx, MaxLat: cy + dy}
	v.ZoomOutURL = "/search?" + wider.Query().Encode()
	clear := Filter{BBox: f.BBox, HasBBox: f.HasBBox, Sort: "recommended", Page: 1}
	v.ClearURL = "/search?" + clear.Query().Encode()
	v.EmptyText = emptyText(f)
	v.Where = "in this area"
	if pl, ok := f.Place(); ok {
		v.Near, v.PlaceName = pl.Slug, pl.Name
		v.NearLat, v.NearLng = strconv.FormatFloat(pl.Point.Lat, 'f', 5, 64), strconv.FormatFloat(pl.Point.Lng, 'f', 5, 64)
		if f.Radius > 0 {
			v.Radius = strconv.Itoa(f.Radius)
			v.Where = "within " + v.Radius + " km of " + pl.Name
		}
	}
	for _, km := range Radii {
		v.RadiusOpts = append(v.RadiusOpts, c.Option{Value: strconv.Itoa(km), Label: strconv.Itoa(km) + " km"})
	}
	return v
}

// Places answers the search box: up to 7 matching places.
func (h *Handler) Places(w http.ResponseWriter, r *http.Request) {
	var opts []pages.PlaceOption
	for _, p := range geo.SearchPlaces(r.URL.Query().Get("q"), 7) {
		opts = append(opts, pages.PlaceOption{Slug: p.Slug, Name: p.Name, Kind: p.Kind.Label(), Area: p.Area,
			Lat: strconv.FormatFloat(p.Point.Lat, 'f', 5, 64), Lng: strconv.FormatFloat(p.Point.Lng, 'f', 5, 64)})
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	render.Component(w, r, http.StatusOK, pages.PlaceSuggestions(opts))
}

// emptyText says what wasn't found, in the renter's words:
// "No self-contained rooms under ₵800 in this area".
func emptyText(f Filter) string {
	what := "places"
	if len(f.Groups) == 1 {
		for _, g := range TypeGroups {
			if g.Key == f.Groups[0] {
				what = strings.ToLower(g.Label)
			}
		}
	}
	if f.SC {
		what = "self-contained " + what
	}
	if f.Furnished {
		what = "furnished " + what
	}
	s := "No " + what
	switch {
	case f.MaxPrice > 0 && f.MinPrice > 0:
		s += " between ₵" + strconv.Itoa(f.MinPrice) + " and ₵" + strconv.Itoa(f.MaxPrice) + " a month"
	case f.MaxPrice > 0:
		s += " under ₵" + strconv.Itoa(f.MaxPrice) + " a month"
	case f.MinPrice > 0:
		s += " over ₵" + strconv.Itoa(f.MinPrice) + " a month"
	}
	if pl, ok := f.Place(); ok && f.Radius > 0 {
		return s + " within " + strconv.Itoa(f.Radius) + " km of " + pl.Name
	}
	return s + " in this area"
}

// cards turns live listings into result cards, looking up whether each
// lister's ID is checked in one query.
func (h *Handler) cards(ctx context.Context, items []*Item, ref geo.Place, saved []uuid.UUID) []partials.ResultCard {
	verified := map[uuid.UUID]bool{}
	var listers []uuid.UUID
	for _, d := range items {
		listers = append(listers, d.L.ListerID)
	}
	if len(listers) > 0 {
		us, err := h.svc.db.User.Query().Where(user.IDIn(listers...), user.IdentityVerifiedAtNotNil()).IDs(ctx)
		if err != nil {
			slog.WarnContext(ctx, "search: verified listers", "err", err)
		}
		for _, id := range us {
			verified[id] = true
		}
	}
	now := time.Now().UTC()
	out := make([]partials.ResultCard, 0, len(items))
	for _, d := range items {
		rc := resultCard(d, verified[d.L.ListerID], now, ref)
		rc.Saved = slices.Contains(saved, d.L.ID)
		out = append(out, rc)
	}
	return out
}

func resultCard(d *Item, verified bool, now time.Time, ref geo.Place) partials.ResultCard {
	r := partials.ResultCard{ID: d.L.ID.String(), URL: PublicPath(d), Headline: d.L.Headline, Verified: verified,
		IsAgent: d.L.ListerKind == listing.ListerKindAgent, Photos: len(d.Photos())}
	if r.Headline == "" {
		r.Headline = Title(d)
	}
	meta := []string{UnitTypeLabel(d.U.UnitType), area(d)}
	if d.P.ApproxLat != nil && d.P.ApproxLng != nil {
		meta = append(meta, geo.PublicDistance(geo.Distance(geo.Point{Lat: *d.P.ApproxLat, Lng: *d.P.ApproxLng}, ref.Point))+" from "+ref.Name)
	}
	r.Meta = strings.Join(meta, " · ")
	if cv := d.Cover(); cv != nil {
		r.Small, r.Medium, r.Blurhash = MediaURL(cv.ID, "w320.jpg"), MediaURL(cv.ID, "w800.jpg"), cv.Blurhash
	}
	if t := d.T; t.Rent != nil {
		r.Price = t.Rent.String()
		if p, ok := rentPeriod(t.RentPeriod); ok {
			r.Per = p.Per
		}
		if m := ComputeMoveIn(d.Terms()); m.Complete {
			r.MoveIn = m.Total.String()
		}
	}
	r.Fresh, r.FreshRecent = freshness(d, now)
	return r
}
