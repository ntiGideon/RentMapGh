package listings

import (
	"encoding/json"
	"encoding/xml"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/platform/money"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

// Area pages for search engines: /kumasi/{place}/{kind}, e.g.
// /kumasi/ayeduase/rooms-for-rent or /kumasi/knust/hostels. Server-rendered
// lists of live listings within AreaRadius of the place.

// AreaRadius is how far an area page reaches, in km.
const AreaRadius = 2

// SEOKind is one of the page families.
type SEOKind struct {
	Slug, Group string // URL segment, TypeGroups key
	Title       string // "Rooms for rent"
	Plural      string // "rooms"
}

var SEOKinds = []SEOKind{
	{"rooms-for-rent", "rooms", "Rooms for rent", "rooms"},
	{"hostels", "hostels", "Hostels", "hostels"},
	{"apartments-for-rent", "apartments", "Apartments and houses for rent", "apartments and houses"},
	{"shops-for-rent", "commercial", "Shops and offices for rent", "shops and offices"},
}

func seoKind(slug string) (SEOKind, bool) {
	i := slices.IndexFunc(SEOKinds, func(k SEOKind) bool { return k.Slug == slug })
	if i < 0 {
		return SEOKind{}, false
	}
	return SEOKinds[i], true
}

// AreaPath is the canonical path of an area page.
func AreaPath(place, kind string) string { return "/kumasi/" + place + "/" + kind }

func areaTitle(k SEOKind, p geo.Place) string {
	prep := "in"
	if p.Kind != geo.KindArea {
		prep = "near" // "Hostels near KNUST", "Rooms for rent in Ayeduase"
	}
	return k.Title + " " + prep + " " + p.Name + ", Kumasi"
}

// AreaPage renders one area page.
func (h *Handler) AreaPage(w http.ResponseWriter, r *http.Request) {
	p, ok := geo.PlaceBySlug(chi.URLParam(r, "place"))
	k, ok2 := seoKind(chi.URLParam(r, "kind"))
	if !ok || !ok2 {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	f := Filter{Near: p.Slug, Radius: AreaRadius, Groups: []string{k.Group}, Sort: "recommended", Page: 1, BBox: DefaultBBox}
	res, err := h.svc.Search(r.Context(), f)
	if err != nil {
		slog.ErrorContext(r.Context(), "area page", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	v := pages.AreaView{
		Title: areaTitle(k, p), Place: p.Name, Kind: k.Plural, Total: res.Total, Radius: AreaRadius,
		Cards:  h.cards(r.Context(), res.Items, p, h.savedFor(w, r)),
		MapURL: "/search?" + f.Query().Encode(),
	}
	v.Intro = areaIntro(res, k, p)
	for _, other := range SEOKinds {
		if other.Slug != k.Slug {
			v.OtherKinds = append(v.OtherKinds, pages.Link{Label: other.Title + " " + prepFor(p) + " " + p.Name, URL: AreaPath(p.Slug, other.Slug)})
		}
	}
	for _, n := range nearestPlaces(p, 6) {
		v.Nearby = append(v.Nearby, pages.Link{Label: k.Title + " " + prepFor(n) + " " + n.Name, URL: AreaPath(n.Slug, k.Slug)})
	}
	abs := h.baseURL + AreaPath(p.Slug, k.Slug)
	m := layouts.Meta{Title: v.Title, Description: v.Intro, URL: abs, NoIndex: res.Total == 0,
		JSONLD: areaJSONLD(h.baseURL, v, res.Items), Modules: []string{"js/compare.js"}}
	if len(v.Cards) > 0 && v.Cards[0].Medium != "" {
		m.Image = h.baseURL + v.Cards[0].Medium
	}
	if len(r.Cookies()) == 0 {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	w.Header().Add("Vary", "Cookie")
	render.Component(w, r, http.StatusOK, pages.Area(m, v))
}

func prepFor(p geo.Place) string {
	if p.Kind == geo.KindArea {
		return "in"
	}
	return "near"
}

// nearestPlaces lists the n places closest to p (not p itself).
func nearestPlaces(p geo.Place, n int) []geo.Place {
	others := slices.DeleteFunc(slices.Clone(geo.Places), func(o geo.Place) bool { return o.Slug == p.Slug })
	sort.Slice(others, func(i, j int) bool {
		return geo.Distance(p.Point, others[i].Point) < geo.Distance(p.Point, others[j].Point)
	})
	return others[:min(n, len(others))]
}

// areaIntro is the one-paragraph summary search engines show: how many,
// and what they cost per month.
func areaIntro(res Results, k SEOKind, p geo.Place) string {
	where := prepFor(p) + " " + p.Name
	if res.Total == 0 {
		return "No " + k.Plural + " listed " + where + " right now. RentMap shows every fee up front, so you know the full move-in cost before you call."
	}
	var monthly []money.Pesewas
	for _, d := range res.Items {
		if d.T.MonthlyEquivalent != nil {
			monthly = append(monthly, *d.T.MonthlyEquivalent)
		}
	}
	s := strconv.Itoa(res.Total) + " " + k.Plural + " " + where + ", Kumasi"
	if len(monthly) > 0 {
		slices.Sort(monthly)
		lo, hi := monthly[0], monthly[len(monthly)-1]
		switch {
		case res.Total > len(res.Items): // only one page loaded: the top end isn't known
			s += ", from " + lo.String() + " a month"
		case lo == hi:
			s += ", about " + lo.String() + " a month"
		default:
			s += ", from " + lo.String() + " to " + hi.String() + " a month"
		}
	}
	return s + ". Every listing shows the full move-in cost — advance, deposit and fees — and availability confirmed by the lister."
}

// ── Structured data ──────────────────────────────────────────────────────

func areaJSONLD(base string, v pages.AreaView, items []*Item) []byte {
	var list []map[string]any
	for i, d := range items {
		list = append(list, map[string]any{"@type": "ListItem", "position": i + 1, "url": base + PublicPath(d), "name": Title(d)})
	}
	doc := []map[string]any{
		{"@context": "https://schema.org", "@type": "ItemList", "name": v.Title, "numberOfItems": v.Total, "itemListElement": list},
		{"@context": "https://schema.org", "@type": "BreadcrumbList", "itemListElement": []map[string]any{
			{"@type": "ListItem", "position": 1, "name": "Kumasi", "item": base + "/search"},
			{"@type": "ListItem", "position": 2, "name": v.Title},
		}},
	}
	b, _ := json.Marshal(doc)
	return b
}

// listingJSONLD describes a live listing: an Offer for an Accommodation,
// located by town only (never coordinates).
func listingJSONLD(base string, v pages.ListingPageView, d *Item) []byte {
	acc := map[string]any{
		"@type": "Accommodation", "name": v.Headline, "description": v.Description,
		"address": map[string]any{"@type": "PostalAddress", "addressLocality": v.Area + ", " + v.City, "addressRegion": d.P.Region, "addressCountry": "GH"},
	}
	var imgs []string
	for _, p := range v.Photos {
		imgs = append(imgs, base+p.Medium)
		if len(imgs) == 4 {
			break
		}
	}
	if len(imgs) > 0 {
		acc["image"] = imgs
	}
	offer := map[string]any{"@context": "https://schema.org", "@type": "Offer", "url": v.PageURL, "itemOffered": acc,
		"availability": "https://schema.org/InStock", "businessFunction": "http://purl.org/goodrelations/v1#LeaseOut"}
	if d.T.Rent != nil {
		offer["priceCurrency"] = "GHS"
		offer["price"] = strconv.FormatFloat(float64(*d.T.Rent)/100, 'f', 2, 64)
		if p, ok := rentPeriod(d.T.RentPeriod); ok {
			offer["priceSpecification"] = map[string]any{"@type": "UnitPriceSpecification", "price": offer["price"],
				"priceCurrency": "GHS", "unitText": p.Per}
		}
	}
	b, _ := json.Marshal(offer)
	return b
}

// ── Sitemap ──────────────────────────────────────────────────────────────

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// Sitemap lists the home page, search, area pages that have listings, and
// every live listing.
// InfoPages are the static help and legal pages (served by the server
// package), listed in the sitemap.
var InfoPages = []string{"/how-we-verify", "/safety", "/guidelines", "/terms", "/privacy"}

func (h *Handler) Sitemap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	urls := []sitemapURL{{Loc: h.baseURL + "/"}, {Loc: h.baseURL + "/search"}}
	for _, p := range InfoPages {
		urls = append(urls, sitemapURL{Loc: h.baseURL + p})
	}
	for _, p := range geo.Places {
		for _, k := range SEOKinds {
			n, err := h.svc.Count(ctx, Filter{Near: p.Slug, Radius: AreaRadius, Groups: []string{k.Group}, BBox: DefaultBBox})
			if err != nil {
				slog.ErrorContext(ctx, "sitemap: count", "err", err)
				continue
			}
			if n > 0 {
				urls = append(urls, sitemapURL{Loc: h.baseURL + AreaPath(p.Slug, k.Slug)})
			}
		}
	}
	ls, err := h.svc.db.Listing.Query().Where(listing.StatusEQ(listing.StatusActive)).Select(listing.FieldID).
		Order(listing.ByUpdatedAt()).Limit(40000).All(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "sitemap: listings", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	ids := make([]uuid.UUID, 0, len(ls))
	for _, l := range ls {
		ids = append(ids, l.ID)
	}
	items, err := h.svc.loadPublic(ctx, ids)
	if err != nil {
		slog.ErrorContext(ctx, "sitemap: load", "err", err)
	}
	for _, d := range items {
		u := sitemapURL{Loc: h.baseURL + PublicPath(d)}
		if t := d.L.LastConfirmedAt; t != nil {
			u.LastMod = t.UTC().Format(time.DateOnly)
		}
		urls = append(urls, u)
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	_ = enc.Encode(struct {
		XMLName xml.Name     `xml:"urlset"`
		NS      string       `xml:"xmlns,attr"`
		URLs    []sitemapURL `xml:"url"`
	}{NS: "http://www.sitemaps.org/schemas/sitemap/0.9", URLs: urls})
}

// Robots allows the public pages and keeps crawlers out of private ones.
func (h *Handler) Robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("User-agent: *\nAllow: /\n" +
		"Disallow: /listings/\nDisallow: /account\nDisallow: /admin/\nDisallow: /m/\nDisallow: /saved\nDisallow: /compare\n" +
		"Disallow: /search?\nDisallow: /search/\nDisallow: /places\n" +
		"\nSitemap: " + h.baseURL + "/sitemap.xml\n"))
}
