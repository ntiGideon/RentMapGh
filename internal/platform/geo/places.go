package geo

import (
	"slices"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Place is somewhere renters search around: a neighbourhood, a campus, a
// hospital, a market or a junction. Aliases are what people actually type
// ("Tech junction", "KATH", "Kejetia").
//
// Coordinates are landmark-level (±300 m). That is enough to centre a
// search and to show rounded distances (100 m / 0.5 km steps, §6.1), but
// they should be checked on the ground before launch.
type Place struct {
	Slug, Name string
	Kind       PlaceKind
	Point      Point
	Aliases    []string
	Area       string // what the suggestion shows under the name
}

type PlaceKind string

const (
	KindArea       PlaceKind = "area"
	KindUniversity PlaceKind = "university"
	KindHospital   PlaceKind = "hospital"
	KindMarket     PlaceKind = "market"
	KindTransport  PlaceKind = "transport"
	KindLandmark   PlaceKind = "landmark"
)

// KindLabel is the small grey word in suggestions.
func (k PlaceKind) Label() string {
	switch k {
	case KindUniversity:
		return "Campus"
	case KindHospital:
		return "Hospital"
	case KindMarket:
		return "Market"
	case KindTransport:
		return "Junction"
	case KindLandmark:
		return "Landmark"
	}
	return "Area"
}

// landmarks are the non-neighbourhood places.
var landmarks = []Place{
	{"knust", "KNUST", KindUniversity, KNUST, []string{"Kwame Nkrumah University of Science and Technology", "Tech", "Knust campus"}, "Ayeduase / Bomso"},
	{"tech-junction", "Tech Junction", KindTransport, Point{6.6857, -1.5770}, []string{"KNUST main gate", "Tech gate"}, "Accra Road"},
	{"kstu", "Kumasi Technical University", KindUniversity, Point{6.6936, -1.6106}, []string{"KsTU", "Kumasi Poly", "Kumasi Polytechnic"}, "Amakom"},
	{"aamusted", "AAMUSTED Kumasi campus", KindUniversity, Point{6.6976, -1.6787}, []string{"UEW Kumasi", "Akenten Appiah-Menka University", "Tanoso campus"}, "Tanoso"},
	{"csuc", "Christian Service University", KindUniversity, Point{6.6726, -1.6620}, []string{"CSUC", "CSU"}, "Santasi"},
	{"kath", "Komfo Anokye Teaching Hospital", KindHospital, Point{6.6975, -1.6311}, []string{"KATH", "Komfo Anokye"}, "Bantama"},
	{"knust-hospital", "KNUST Hospital", KindHospital, Point{6.6800, -1.5725}, []string{"University Hospital"}, "KNUST"},
	{"kejetia", "Kejetia Market", KindMarket, Point{6.6998, -1.6232}, []string{"Kejetia", "Central Market", "Kumasi Central Market"}, "Adum"},
	{"kumasi-city-mall", "Kumasi City Mall", KindLandmark, Point{6.6727, -1.6093}, []string{"City Mall", "Shoprite Asokwa"}, "Asokwa"},
	{"baba-yara", "Baba Yara Stadium", KindLandmark, Point{6.6892, -1.6363}, []string{"Kumasi Sports Stadium"}, "Asokwa"},
	{"manhyia", "Manhyia Palace", KindLandmark, Point{6.7063, -1.6175}, []string{"Manhyia"}, "Manhyia"},
	{"kumasi-airport", "Kumasi Airport", KindTransport, Point{6.7147, -1.5909}, []string{"Prempeh I Airport", "Airport"}, "Buokrom"},
	{"anloga-junction", "Anloga Junction", KindTransport, Point{6.6905, -1.6005}, []string{"Anloga"}, "Accra Road"},
	{"sofoline", "Sofoline Interchange", KindTransport, Point{6.6980, -1.6450}, []string{"Sofoline"}, "Bantama"},
}

// Places is every searchable place: the neighbourhoods, then landmarks.
var Places = func() []Place {
	var out []Place
	for _, n := range Neighbourhoods {
		out = append(out, Place{Slug: n.Slug, Name: n.Label, Kind: KindArea, Point: n.Centre, Area: "Kumasi"})
	}
	return append(out, landmarks...)
}()

// PlaceBySlug looks up a place.
func PlaceBySlug(slug string) (Place, bool) {
	i := slices.IndexFunc(Places, func(p Place) bool { return p.Slug == slug })
	if i < 0 {
		return Place{}, false
	}
	return Places[i], true
}

// fold lowercases and strips accents and punctuation: "Kóteí-Junction" → "kotei junction".
func fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	s, _, _ = transform.String(t, s)
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space && b.Len() > 0 {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// SearchPlaces returns up to n places matching q, best first: an exact
// name or alias, then a name that starts with q, then an alias that does,
// then a word inside either.
func SearchPlaces(q string, n int) []Place {
	q = fold(q)
	if q == "" {
		return nil
	}
	type hit struct {
		p     Place
		score int
	}
	var hits []hit
	for _, p := range Places {
		best := 0
		// An exact name or alias wins outright: "tech" is what people call
		// KNUST, even though "Tech Junction" starts with it.
		consider := func(s string, prefix, inner int) {
			f := fold(s)
			switch {
			case f == q:
				best = max(best, 50+prefix/10)
			case strings.HasPrefix(f, q):
				best = max(best, prefix)
			case strings.Contains(" "+f, " "+q):
				best = max(best, inner)
			}
		}
		consider(p.Name, 40, 20)
		for _, a := range p.Aliases {
			consider(a, 30, 10)
		}
		if best > 0 {
			if p.Kind != KindArea {
				best += 2 // "tech" should find KNUST before an area named Tech-something
			}
			hits = append(hits, hit{p, best})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var out []Place
	for _, h := range hits {
		if len(out) == n {
			break
		}
		out = append(out, h.p)
	}
	return out
}

// Nearby is a place with its distance from a point.
type Nearby struct {
	Place  Place
	Metres float64
}

// NearbyPlaces lists landmarks (not neighbourhoods) within maxMetres of p,
// nearest first, at most n.
func NearbyPlaces(p Point, maxMetres float64, n int) []Nearby {
	var out []Nearby
	for _, pl := range landmarks {
		if d := Distance(p, pl.Point); d <= maxMetres {
			out = append(out, Nearby{pl, d})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metres < out[j].Metres })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
