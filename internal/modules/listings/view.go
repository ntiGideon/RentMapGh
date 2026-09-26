package listings

import (
	"net/url"
	"strconv"
	"time"

	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/platform/money"
	c "rentmapgh/internal/views/components"
	"rentmapgh/internal/views/partials"
)

func toOpts(opts []Option) []c.Option {
	out := make([]c.Option, len(opts))
	for i, o := range opts {
		out[i] = c.Option{Value: o.Value, Label: o.Label}
	}
	return out
}

var (
	neighbourhoodOpts = func() []c.Option {
		out := make([]c.Option, len(geo.Neighbourhoods))
		for i, n := range geo.Neighbourhoods {
			out[i] = c.Option{Value: n.Slug, Label: n.Label}
		}
		return out
	}()
	periodOpts = func() []c.Option {
		out := make([]c.Option, len(RentPeriods))
		for i, p := range RentPeriods {
			out[i] = c.Option{Value: p.Key, Label: p.Label}
		}
		return out
	}()
	unitGroups = func() []partials.OptionGroup {
		var groups []partials.OptionGroup
		idx := map[string]int{}
		for _, u := range UnitTypes {
			i, ok := idx[u.Group]
			if !ok {
				i = len(groups)
				idx[u.Group] = i
				groups = append(groups, partials.OptionGroup{Label: u.Group})
			}
			g := &groups[i]
			g.Options = append(g.Options, c.Option{Value: u.Key, Label: u.Label})
			if u.Bedrooms < 0 {
				g.AskBedrooms = append(g.AskBedrooms, u.Key)
			}
			if !u.Residential {
				g.Commercial = append(g.Commercial, u.Key)
			}
		}
		return groups
	}()
	amenityGroups = func() []partials.OptionGroup {
		var groups []partials.OptionGroup
		idx := map[string]int{}
		for _, a := range Amenities {
			i, ok := idx[a.Group]
			if !ok {
				i = len(groups)
				idx[a.Group] = i
				groups = append(groups, partials.OptionGroup{Label: a.Group})
			}
			groups[i].Options = append(groups[i].Options, c.Option{Value: a.Key, Label: a.Label})
		}
		return groups
	}()
)

func fmtFloat(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', 6, 64)
}

func fmtInt(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func fmtMoney(p *money.Pesewas) string {
	if p == nil {
		return ""
	}
	return p.Input()
}

// Title is what the listing is called in lists and the wizard header.
func Title(d *Item) string {
	if d.L.Headline != "" {
		return d.L.Headline
	}
	if d.U != nil && d.U.UnitType != "" {
		t := UnitTypeLabel(d.U.UnitType)
		if n, ok := geo.NeighbourhoodBySlug(d.P.Neighbourhood); ok {
			t += " in " + n.Label
		}
		return t
	}
	return "New listing"
}

func moveInView(t Terms) partials.MoveInView {
	m := ComputeMoveIn(t)
	v := partials.MoveInView{Total: m.Total.String(), Monthly: m.Monthly.String(), Complete: m.Complete, Missing: m.Missing, HasRent: t.Rent != nil}
	for _, l := range m.Lines {
		v.Lines = append(v.Lines, partials.MoneyLine{Label: l.Label, Amount: l.Amount.String()})
	}
	return v
}

// buildView fills the wizard view from stored data, then overlays the
// submitted form (so a failed Continue keeps what the lister typed).
func buildView(d *Item, step string, identityVerified bool, form url.Values, errs ValidationError, sharedUnits int) partials.WizardView {
	v := partials.WizardView{
		ID: d.L.ID.String(), Step: step, Title: Title(d), Status: string(d.L.Status), ReviewNote: d.L.ReviewNote,
		IsAgent: d.L.ListerKind == "agent", SharedProperty: sharedUnits > 1, Errors: errs,
		IdentityVerified: identityVerified, Today: time.Now().Format("2006-01-02"),

		Lat: fmtFloat(d.P.Lat), Lng: fmtFloat(d.P.Lng),
		DefaultLat: strconv.FormatFloat(geo.KNUST.Lat, 'f', 5, 64), DefaultLng: strconv.FormatFloat(geo.KNUST.Lng, 'f', 5, 64),
		DigitalAddress: d.P.DigitalAddress, Landmark: d.P.Landmark, Street: d.P.Street, Neighbourhood: d.P.Neighbourhood,
		Neighbourhoods: neighbourhoodOpts,

		Category: d.P.Category, Name: d.P.Name, Categories: toOpts(Categories),

		UnitType: d.U.UnitType, Bedrooms: fmtInt(d.U.Bedrooms), Bathrooms: fmtInt(d.U.Bathrooms), SizeSqm: fmtInt(d.U.SizeSqm),
		Floor: fmtInt(d.U.Floor), Label: d.U.Label, Furnished: d.U.Furnished, Meter: d.U.MeterType, Water: d.U.WaterSource,
		Kitchen: d.U.Kitchen, UnitGroups: unitGroups, FurnishedOpts: toOpts(FurnishedOptions), MeterOpts: toOpts(MeterOptions),
		WaterOpts: toOpts(WaterOptions), KitchenOpts: toOpts(KitchenOptions),

		Amenities: d.U.Amenities, AmenityGroups: amenityGroups,

		Photos: photosView(d, errs["photos"]),

		Rent: fmtMoney(d.T.Rent), Period: d.T.RentPeriod, Advance: fmtInt(d.T.AdvancePeriods), Deposit: fmtMoney(d.T.Deposit),
		AgentFee: fmtMoney(d.T.AgentFee), ServiceCharge: fmtMoney(d.T.ServiceCharge), ViewingFee: fmtMoney(d.T.ViewingFee),
		MinLease: fmtInt(d.T.MinLeaseMonths), Negotiable: d.T.Negotiable, Periods: periodOpts, MoveIn: moveInView(d.Terms()),

		Headline: d.L.Headline, Description: d.L.Description,
	}
	if d.U.SelfContained != nil {
		v.SelfContained = map[bool]string{true: "yes", false: "no"}[*d.U.SelfContained]
	}
	if d.L.AvailableFrom != nil {
		v.AvailableFrom = d.L.AvailableFrom.Format("2006-01-02")
	}
	for _, f := range d.T.OtherFees {
		v.Fees = append(v.Fees, partials.FeeRow{Label: f.Label, Amount: f.Amount.Input()})
	}
	for len(v.Fees) < 3 {
		v.Fees = append(v.Fees, partials.FeeRow{})
	}

	// Stepper: done = before the furthest step reached.
	reached := stepIndex(d.L.WizardStep)
	cur := stepIndex(step)
	for i, s := range Steps {
		st := partials.WizStep{Key: s.Key, Label: s.Label, Hint: s.Hint, Reachable: i <= reached}
		switch {
		case i == cur:
			st.State = "current"
		case i < reached:
			st.State = "done"
		default:
			st.State = "todo"
		}
		v.Steps = append(v.Steps, st)
	}
	v.StepNumber = cur + 1

	if step == "review" {
		score, tips := Quality(QualityOf(d))
		v.Score = score
		for _, t := range tips {
			v.Tips = append(v.Tips, partials.TipView{Text: t.Text, Step: t.Step, Points: t.Points})
		}
		for _, m := range Missing(d) {
			v.Missing = append(v.Missing, partials.ReqView{Step: m.Step, Text: m.Text})
		}
		v.Summary = summary(d)
	}

	overlay(&v, form)
	return v
}

// overlay copies posted values back into the view after a failed submit.
func overlay(v *partials.WizardView, f url.Values) {
	if f == nil {
		return
	}
	set := func(dst *string, key string) {
		if f.Has(key) {
			*dst = f.Get(key)
		}
	}
	for dst, key := range map[*string]string{
		&v.Lat: "lat", &v.Lng: "lng", &v.DigitalAddress: "digital_address", &v.Landmark: "landmark", &v.Street: "street",
		&v.Neighbourhood: "neighbourhood", &v.Category: "category", &v.Name: "name", &v.UnitType: "unit_type",
		&v.Bedrooms: "bedrooms", &v.Bathrooms: "bathrooms", &v.SizeSqm: "size_sqm", &v.Floor: "floor", &v.Label: "label",
		&v.Furnished: "furnished", &v.SelfContained: "self_contained", &v.Meter: "meter_type", &v.Water: "water_source",
		&v.Kitchen: "kitchen", &v.Rent: "rent", &v.Period: "rent_period", &v.Advance: "advance_periods",
		&v.Deposit: "deposit", &v.AgentFee: "agent_fee", &v.ServiceCharge: "service_charge", &v.ViewingFee: "viewing_fee",
		&v.MinLease: "min_lease_months", &v.Headline: "headline", &v.Description: "description", &v.AvailableFrom: "available_from",
	} {
		set(dst, key)
	}
	if f.Has("rent") {
		v.Negotiable = f.Get("negotiable") == "1"
		for i := range v.Fees {
			n := strconv.Itoa(i + 1)
			v.Fees[i] = partials.FeeRow{Label: f.Get("fee_label_" + n), Amount: f.Get("fee_amount_" + n)}
		}
	}
	if _, ok := f["amenities"]; ok {
		v.Amenities = f["amenities"]
	}
}

func summary(d *Item) []partials.SummarySection {
	loc := partials.SummarySection{Title: "Location", Step: "location"}
	area := "—"
	if n, ok := geo.NeighbourhoodBySlug(d.P.Neighbourhood); ok {
		area = n.Label
	}
	loc.Rows = append(loc.Rows, partials.SummaryRow{Label: "Area", Value: area},
		partials.SummaryRow{Label: "Landmark", Value: orDash(d.P.Landmark)},
		partials.SummaryRow{Label: "Digital address", Value: orDash(d.P.DigitalAddress)},
		partials.SummaryRow{Label: "Map pin", Value: map[bool]string{true: "Set — renters see the approximate area", false: "Not set"}[d.P.Lat != nil]})

	unitSec := partials.SummarySection{Title: "The space", Step: "unit"}
	unitSec.Rows = append(unitSec.Rows,
		partials.SummaryRow{Label: "Type", Value: orDash(UnitTypeLabel(d.U.UnitType))},
		partials.SummaryRow{Label: "Bedrooms", Value: orDash(fmtInt(d.U.Bedrooms))},
		partials.SummaryRow{Label: "Self-contained", Value: yesNo(d.U.SelfContained)},
		partials.SummaryRow{Label: "Furnishing", Value: orDash(OptionLabel(FurnishedOptions, d.U.Furnished))},
		partials.SummaryRow{Label: "Electricity", Value: orDash(OptionLabel(MeterOptions, d.U.MeterType))},
		partials.SummaryRow{Label: "Water", Value: orDash(OptionLabel(WaterOptions, d.U.WaterSource))},
		partials.SummaryRow{Label: "Kitchen", Value: orDash(OptionLabel(KitchenOptions, d.U.Kitchen))},
		partials.SummaryRow{Label: "Amenities", Value: strconv.Itoa(len(d.U.Amenities)) + " ticked"})

	photos := partials.SummarySection{Title: "Photos", Step: "photos"}
	count := "None yet"
	if n := len(d.Photos()); n > 0 {
		count = strconv.Itoa(n) + " — the first is the cover"
	}
	photos.Rows = append(photos.Rows, partials.SummaryRow{Label: "Photos", Value: count})

	det := partials.SummarySection{Title: "Details", Step: "details"}
	avail := "Now"
	if d.L.AvailableFrom != nil {
		avail = d.L.AvailableFrom.Format("2 January 2006")
	}
	det.Rows = append(det.Rows, partials.SummaryRow{Label: "Headline", Value: orDash(d.L.Headline)},
		partials.SummaryRow{Label: "Available", Value: avail},
		partials.SummaryRow{Label: "Description", Value: strconv.Itoa(runeLen(d.L.Description)) + " characters"})
	return []partials.SummarySection{loc, unitSec, photos, det}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func yesNo(b *bool) string {
	if b == nil {
		return "—"
	}
	if *b {
		return "Yes"
	}
	return "No"
}

// cardView is one row on "Your listings".
func cardView(d *Item, now time.Time) partials.ListingCardView {
	st := Status(d.L.Status)
	cv := partials.ListingCardView{
		ID: d.L.ID.String(), Title: Title(d), Status: string(st), Score: d.L.QualityScore,
		ReviewNote: d.L.ReviewNote, Updated: ago(d.L.UpdatedAt, now),
	}
	var parts []string
	if n, ok := geo.NeighbourhoodBySlug(d.P.Neighbourhood); ok {
		parts = append(parts, n.Label)
	}
	if d.U.Label != "" {
		parts = append(parts, d.U.Label)
	}
	if d.U.UnitType != "" && d.L.Headline != "" {
		parts = append(parts, UnitTypeLabel(d.U.UnitType))
	}
	cv.Subtitle = join(parts, " · ")
	if d.T.Rent != nil {
		cv.Rent = d.T.Rent.String()
		if p, ok := rentPeriod(d.T.RentPeriod); ok {
			cv.Per = p.Per
		}
		if m := ComputeMoveIn(d.Terms()); m.Complete {
			cv.MoveIn = m.Total.String()
		}
	}
	cv.EditURL, cv.EditLabel = "/listings/"+cv.ID+"/edit/"+d.L.WizardStep, "Continue"
	if st != Draft {
		cv.EditURL, cv.EditLabel = "/listings/"+cv.ID+"/edit/review", "Edit"
	}
	switch st {
	case Active:
		cv.Actions = []partials.ListingAction{{Event: string(EvPause), Label: "Pause"}, {Event: string(EvMarkRented), Label: "Mark rented"}}
	case Paused:
		cv.Actions = []partials.ListingAction{{Event: string(EvResume), Label: "Resume"}, {Event: string(EvMarkRented), Label: "Mark rented"}}
	case Rented:
		cv.Actions = []partials.ListingAction{{Event: string(EvRelist), Label: "Relist"}}
	case PendingReview:
		cv.Actions = []partials.ListingAction{{Event: string(EvWithdraw), Label: "Withdraw"}}
	case Expired:
		cv.Actions = []partials.ListingAction{{Event: string(EvResume), Label: "Still available"}}
	}
	cv.CanAddUnit = d.P.Lat != nil
	if c := d.Cover(); c != nil {
		cv.CoverURL = MediaURL(c.ID, "w320.jpg")
	}
	cv.Photos = len(d.Photos())
	return cv
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h ago"
	case d < 48*time.Hour:
		return "yesterday"
	}
	return t.Format("2 Jan")
}

// photosView is the photo manager: tiles in order, plus the counts that
// drive its hints.
func photosView(d *Item, errMsg string) partials.PhotosView {
	v := partials.PhotosView{
		ListingID: d.L.ID.String(), Min: MinPhotos, Good: GoodPhotos, Max: MaxPhotos,
		Editable: Editable(Status(d.L.Status)), Error: errMsg,
	}
	for _, m := range d.Photos() {
		v.Tiles = append(v.Tiles, partials.PhotoTile{
			ID: m.ID.String(), Blurhash: m.Blurhash, Width: m.Width, Height: m.Height,
			Small: MediaURL(m.ID, "w320.jpg"), Medium: MediaURL(m.ID, "w800.jpg"), Large: MediaURL(m.ID, "w1600.jpg"),
		})
	}
	return v
}
