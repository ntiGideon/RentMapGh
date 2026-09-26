// Package listings runs property supply: properties, units, listings and
// their terms, the listing wizard, and the review queue.
package listings

// Option is a value + label pair for forms.
type Option struct{ Value, Label string }

// UnitType is the Ghanaian unit taxonomy (ProjectRequirement §5.2).
type UnitType struct {
	Key, Label, Group string
	Bedrooms          int  // implied bedroom count; -1 = ask
	Residential       bool // false for shops, offices, stores…
	Hostel            bool
}

var UnitTypes = []UnitType{
	{"single_room", "Single room", "Rooms", 1, true, false},
	{"single_room_sc", "Single room, self-contained", "Rooms", 1, true, false},
	{"chamber_hall", "Chamber and hall", "Rooms", 1, true, false},
	{"chamber_hall_sc", "Chamber and hall, self-contained", "Rooms", 1, true, false},
	{"compound_room", "Compound house room", "Rooms", 1, true, false},
	{"boys_quarters", "Boys' quarters", "Rooms", 1, true, false},
	{"hostel_1in1", "Hostel room (1 in a room)", "Hostels", 1, true, true},
	{"hostel_2in1", "Hostel room (2 in a room)", "Hostels", 1, true, true},
	{"hostel_4in1", "Hostel room (4 in a room)", "Hostels", 1, true, true},
	{"apartment_1bed", "1-bedroom apartment", "Apartments & houses", 1, true, false},
	{"apartment_2bed", "2-bedroom apartment", "Apartments & houses", 2, true, false},
	{"apartment_3bed", "3-bedroom apartment", "Apartments & houses", 3, true, false},
	{"apartment_4bed", "4+ bedroom apartment", "Apartments & houses", -1, true, false},
	{"detached_house", "Detached house", "Apartments & houses", -1, true, false},
	{"semi_detached", "Semi-detached house", "Apartments & houses", -1, true, false},
	{"townhouse", "Townhouse", "Apartments & houses", -1, true, false},
	{"shop", "Shop", "Commercial", 0, false, false},
	{"store", "Store / warehouse", "Commercial", 0, false, false},
	{"office", "Office", "Commercial", 0, false, false},
	{"event_space", "Event space", "Commercial", 0, false, false},
}

func unitType(key string) (UnitType, bool) {
	for _, u := range UnitTypes {
		if u.Key == key {
			return u, true
		}
	}
	return UnitType{}, false
}

// UnitTypeLabel returns the display label for a unit type key.
func UnitTypeLabel(key string) string {
	if u, ok := unitType(key); ok {
		return u.Label
	}
	return key
}

// Amenity is a controlled-vocabulary feature. Keys are stored on the unit as
// a JSON array (GIN-indexable for "has wifi" filters in Phase 3).
type Amenity struct{ Key, Label, Group, Icon string }

var Amenities = []Amenity{
	{"gated", "Gated compound", "Security", "shield-check"},
	{"watchman", "Watchman / security guard", "Security", "shield-check"},
	{"cctv", "CCTV", "Security", "shield-check"},
	{"burglar_proof", "Burglar-proof windows", "Security", "shield-check"},
	{"water_storage", "Water storage (poly tank)", "Water & power", "droplet"},
	{"backup_power", "Generator / solar backup", "Water & power", "zap"},
	{"wifi", "Wi-Fi included", "Comfort", "signal"},
	{"ac", "Air conditioning", "Comfort", "sun"},
	{"ceiling_fan", "Ceiling fan", "Comfort", "sun"},
	{"wardrobe", "Built-in wardrobe", "Comfort", "home"},
	{"tiled", "Tiled floors", "Comfort", "home"},
	{"porch", "Porch / balcony", "Comfort", "home"},
	{"parking", "Parking", "Outside", "map-pin"},
	{"laundry_area", "Laundry area", "Outside", "droplet"},
	{"pets_allowed", "Pets allowed", "Outside", "home"},
	{"study_room", "Study room", "Hostels", "home"},
	{"common_kitchen", "Shared kitchen", "Hostels", "home"},
	{"shuttle", "Shuttle to campus", "Hostels", "map"},
}

func isAmenity(key string) bool {
	for _, a := range Amenities {
		if a.Key == key {
			return true
		}
	}
	return false
}

// Categories of property.
var Categories = []Option{
	{"residential", "House or apartment building"},
	{"compound", "Compound house"},
	{"hostel", "Hostel"},
	{"commercial", "Shops or offices"},
	{"mixed", "Mixed (homes + shops)"},
}

var (
	FurnishedOptions = []Option{{"none", "Unfurnished"}, {"semi", "Semi-furnished"}, {"full", "Fully furnished"}}
	MeterOptions     = []Option{{"prepaid_own", "Own prepaid meter"}, {"prepaid_shared", "Shared prepaid meter"}, {"postpaid", "Postpaid (shared bill)"}, {"none", "No electricity"}}
	WaterOptions     = []Option{{"gwcl", "Ghana Water (GWCL)"}, {"borehole", "Borehole"}, {"poly_tank", "Poly tank (refilled)"}, {"mixed", "Mix of sources"}, {"none", "No water on site"}}
	KitchenOptions   = []Option{{"private", "Own kitchen"}, {"shared", "Shared kitchen"}, {"none", "No kitchen"}}
)

// RentPeriod is what one rent payment covers.
type RentPeriod struct {
	Key, Label, Per string
	Months          int // for the monthly equivalent used by every price filter
}

// Semesters and academic years follow the KNUST calendar: two ~4-month
// semesters, and hostels usually bill an 8-month academic year.
var RentPeriods = []RentPeriod{
	{"month", "Per month", "month", 1},
	{"semester", "Per semester", "semester", 4},
	{"academic_year", "Per academic year", "academic year", 8},
	{"year", "Per year", "year", 12},
}

func rentPeriod(key string) (RentPeriod, bool) {
	for _, p := range RentPeriods {
		if p.Key == key {
			return p, true
		}
	}
	return RentPeriod{}, false
}

func optionValid(opts []Option, v string) bool {
	for _, o := range opts {
		if o.Value == v {
			return true
		}
	}
	return false
}

// OptionLabel returns the label of v in opts.
func OptionLabel(opts []Option, v string) string {
	for _, o := range opts {
		if o.Value == v {
			return o.Label
		}
	}
	return v
}
