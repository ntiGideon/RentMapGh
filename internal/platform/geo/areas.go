package geo

// Neighbourhood is a named area. Centres are approximate (for centring the
// map and defaults); real boundaries arrive as polygons in Phase 3.
type Neighbourhood struct {
	Slug, Label string
	Centre      Point
}

// KNUST is the default map centre for the launch area.
var KNUST = Point{Lat: 6.6745, Lng: -1.5716}

// Neighbourhoods around KNUST first (the launch area), then wider Kumasi.
var Neighbourhoods = []Neighbourhood{
	{"ayeduase", "Ayeduase", Point{6.6697, -1.5588}},
	{"bomso", "Bomso", Point{6.6860, -1.5830}},
	{"kotei", "Kotei", Point{6.6630, -1.5530}},
	{"ayigya", "Ayigya", Point{6.6840, -1.5640}},
	{"oduom", "Oduom", Point{6.6590, -1.5420}},
	{"kentinkrono", "Kentinkrono", Point{6.6800, -1.5450}},
	{"boadi", "Boadi", Point{6.6860, -1.5320}},
	{"emena", "Emena", Point{6.6700, -1.5300}},
	{"anloga", "Anloga", Point{6.6900, -1.6000}},
	{"asokwa", "Asokwa", Point{6.6710, -1.6030}},
	{"ahinsan", "Ahinsan", Point{6.6560, -1.5880}},
	{"atonsu", "Atonsu", Point{6.6450, -1.5960}},
	{"adum", "Adum", Point{6.6930, -1.6240}},
	{"bantama", "Bantama", Point{6.7050, -1.6360}},
	{"ahodwo", "Ahodwo", Point{6.6660, -1.6300}},
	{"danyame", "Danyame", Point{6.6760, -1.6320}},
	{"nhyiaeso", "Nhyiaeso", Point{6.6770, -1.6400}},
	{"santasi", "Santasi", Point{6.6670, -1.6570}},
	{"suame", "Suame", Point{6.7210, -1.6280}},
	{"tafo", "Tafo", Point{6.7310, -1.6100}},
	{"tanoso", "Tanoso", Point{6.6900, -1.6800}},
	{"abuakwa", "Abuakwa", Point{6.6960, -1.7150}},
	{"ejisu", "Ejisu", Point{6.7200, -1.4700}},
}

// NeighbourhoodBySlug looks up an area.
func NeighbourhoodBySlug(slug string) (Neighbourhood, bool) {
	for _, n := range Neighbourhoods {
		if n.Slug == slug {
			return n, true
		}
	}
	return Neighbourhood{}, false
}

// Nearest returns the neighbourhood whose centre is closest to p, for
// pre-filling the field after the pin is dropped.
func Nearest(p Point) Neighbourhood {
	best, bestD := Neighbourhoods[0], Distance(p, Neighbourhoods[0].Centre)
	for _, n := range Neighbourhoods[1:] {
		if d := Distance(p, n.Centre); d < bestD {
			best, bestD = n, d
		}
	}
	return best
}
