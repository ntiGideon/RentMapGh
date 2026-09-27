package listings

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/listingmedia"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/modules/mandates"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/views/pages"
	"rentmapgh/internal/views/partials"
)

// Public pages (ProjectRequirement §6.1): everything renters see is built
// from the approximate point. The exact lat/lng, street, digital address
// and landmark stay private — the landmark ("behind the police station")
// and street would narrow the 150–400 m offset down to a building. Property
// names are shown only for hostels and commercial places, which advertise
// themselves anyway.

// publicStatuses can be opened by anyone. Paused, rented and expired pages
// stay reachable (shared links keep working) with an "unavailable" note.
var publicStatuses = []listing.Status{listing.StatusActive, listing.StatusPaused, listing.StatusRented, listing.StatusExpired}

// PublicListing loads a listing for its public page, with its lister.
// Drafts and listings in review are only visible to their own lister
// (preview); everyone else gets ErrNotFound.
func (s *Service) PublicListing(ctx context.Context, id, viewer uuid.UUID) (*Item, *ent.User, error) {
	q := s.db.Listing.Query().Where(listing.ID(id))
	if viewer == uuid.Nil {
		q.Where(listing.StatusIn(publicStatuses...))
	} else {
		q.Where(listing.Or(listing.StatusIn(publicStatuses...), listing.ListerID(viewer)))
	}
	l, err := q.WithTerms().WithUnit(func(q *ent.UnitQuery) { q.WithProperty() }).
		WithMedia(func(q *ent.ListingMediaQuery) {
			withMedia(q)
			q.Where(listingmedia.StatusEQ(listingmedia.StatusReady))
		}).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("public listing: %w", err)
	}
	d := draftOf(l)
	if d.U == nil || d.P == nil || d.T == nil {
		return nil, nil, ErrNotFound
	}
	u, err := s.db.User.Query().Where(user.ID(l.ListerID)).WithAgentProfile().WithLandlordProfile().Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("public listing: lister: %w", err)
	}
	return d, u, nil
}

// Slug is the readable tail of a listing URL: "self-contained-chamber-hall-ayigya".
func Slug(d *Item) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(Title(d)) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// PublicPath is a listing's canonical path.
func PublicPath(d *Item) string {
	if s := Slug(d); s != "" {
		return "/l/" + d.L.ID.String() + "/" + s
	}
	return "/l/" + d.L.ID.String()
}

// area is the public place name: the lister's neighbourhood, else the one
// nearest the approximate point.
func area(d *Item) string {
	if n, ok := geo.NeighbourhoodBySlug(d.P.Neighbourhood); ok {
		return n.Label
	}
	if d.P.ApproxLat != nil && d.P.ApproxLng != nil {
		return geo.Nearest(geo.Point{Lat: *d.P.ApproxLat, Lng: *d.P.ApproxLng}).Label
	}
	return d.P.City
}

// showsName reports whether the property name may be public.
func showsName(d *Item) bool {
	return d.P.Name != "" && (d.P.Category == "hostel" || d.P.Category == "commercial")
}

// freshness words the last confirmation for renters.
func freshness(d *Item, now time.Time) (string, bool) {
	t := d.L.LastConfirmedAt
	if t == nil {
		t = d.L.PublishedAt
	}
	if t == nil {
		return "", false
	}
	days := int(now.Sub(*t).Hours() / 24)
	switch {
	case days <= 0:
		return "Confirmed available today", true
	case days == 1:
		return "Confirmed available yesterday", true
	case days <= 7:
		return "Confirmed available " + strconv.Itoa(days) + " days ago", true
	case days <= 30:
		return "Last confirmed " + strconv.Itoa(days) + " days ago", false
	}
	return "Last confirmed " + t.Format("2 Jan 2006"), false
}

// publicView builds the page. It reads approx_* only; see the note above.
func publicView(d *Item, lister *ent.User, authority mandates.State, viewer uuid.UUID, dataSaver bool, now time.Time) pages.ListingPageView {
	v := pages.ListingPageView{
		ID: d.L.ID.String(), Path: PublicPath(d), Title: Title(d), Headline: d.L.Headline, Description: d.L.Description,
		Status: string(d.L.Status), Area: area(d), City: d.P.City, UnitType: UnitTypeLabel(d.U.UnitType),
		Preview: d.L.ListerID == viewer && d.L.Status != listing.StatusActive,
		MoveIn:  moveInView(d.Terms()),
	}
	if showsName(d) {
		v.PropertyName = d.P.Name
	}
	if v.Headline == "" {
		v.Headline = v.Title
	}
	switch d.L.Status {
	case listing.StatusRented:
		v.Unavailable = "This place has been rented."
	case listing.StatusPaused:
		v.Unavailable = "The lister has paused this listing for now."
	case listing.StatusExpired:
		v.Unavailable = "This listing hasn't been confirmed recently, so it may no longer be available."
	}
	if t := d.T; t.Rent != nil {
		v.Price = t.Rent.String()
		if p, ok := rentPeriod(t.RentPeriod); ok {
			v.Per = p.Per
			if p.Months > 1 && t.MonthlyEquivalent != nil {
				v.Monthly = t.MonthlyEquivalent.String()
			}
		}
		if m := ComputeMoveIn(d.Terms()); m.Complete {
			v.MoveInTotal = m.Total.String()
		}
		v.Negotiable = t.Negotiable
	}
	if d.P.ApproxLat != nil && d.P.ApproxLng != nil {
		approx := geo.Point{Lat: *d.P.ApproxLat, Lng: *d.P.ApproxLng}
		v.ApproxLat = strconv.FormatFloat(approx.Lat, 'f', 5, 64)
		v.ApproxLng = strconv.FormatFloat(approx.Lng, 'f', 5, 64)
		v.Distance = geo.PublicDistance(geo.Distance(approx, geo.KNUST)) + " from KNUST"
		for _, n := range geo.NearbyPlaces(approx, 5000, 4) {
			v.Nearby = append(v.Nearby, pages.NearbyPlace{Name: n.Place.Name, Kind: n.Place.Kind.Label(), Distance: geo.PublicDistance(n.Metres)})
		}
	}
	v.Fresh, v.FreshRecent = freshness(d, now)

	for _, m := range d.Photos() {
		v.Photos = append(v.Photos, partials.PhotoTile{
			ID: m.ID.String(), Blurhash: m.Blurhash, Width: m.Width, Height: m.Height,
			Small: MediaURL(m.ID, "w320.jpg"), Medium: MediaURL(m.ID, "w800.jpg"), Large: MediaURL(m.ID, "w1600.jpg"),
		})
	}
	if d.HasReadyVideo() {
		v.Video = videoView(d, true, dataSaver, "")
		v.Video.Editable = false
	}

	u := d.U
	add := func(label, value, icon string) {
		if value != "" && value != "—" {
			v.Facts = append(v.Facts, pages.Fact{Label: label, Value: value, Icon: icon})
		}
	}
	add("Type", UnitTypeLabel(u.UnitType), "home")
	if u.Bedrooms != nil && *u.Bedrooms > 1 {
		add("Bedrooms", strconv.Itoa(*u.Bedrooms), "bed")
	}
	if u.Bathrooms != nil {
		add("Bathrooms", strconv.Itoa(*u.Bathrooms), "droplet")
	}
	if u.SelfContained != nil {
		add("Self-contained", map[bool]string{true: "Yes — own bath and toilet", false: "No — shared facilities"}[*u.SelfContained], "key")
	}
	add("Furnishing", OptionLabel(FurnishedOptions, u.Furnished), "home")
	add("Electricity", OptionLabel(MeterOptions, u.MeterType), "zap")
	add("Water", OptionLabel(WaterOptions, u.WaterSource), "droplet")
	add("Kitchen", OptionLabel(KitchenOptions, u.Kitchen), "home")
	if u.SizeSqm != nil {
		add("Size", strconv.Itoa(*u.SizeSqm)+" m²", "map")
	}
	avail := "Now"
	if d.L.AvailableFrom != nil && d.L.AvailableFrom.After(now) {
		avail = d.L.AvailableFrom.Format("2 January 2006")
	}
	add("Available", avail, "calendar-check")
	if t := d.T; t.MinLeaseMonths != nil && *t.MinLeaseMonths > 0 {
		add("Minimum stay", strconv.Itoa(*t.MinLeaseMonths)+" months", "clock")
	}
	if u.SelfContained != nil && *u.SelfContained {
		v.Tags = append(v.Tags, "Self-contained")
	}
	if u.Furnished == "full" {
		v.Tags = append(v.Tags, "Furnished")
	}
	if u.MeterType == "prepaid_own" {
		v.Tags = append(v.Tags, "Own meter")
	}

	have := map[string]bool{}
	for _, a := range u.Amenities {
		have[a] = true
	}
	for _, a := range Amenities {
		if !have[a.Key] {
			continue
		}
		if n := len(v.Amenities); n == 0 || v.Amenities[n-1].Title != a.Group {
			v.Amenities = append(v.Amenities, pages.AmenityGroup{Title: a.Group})
		}
		g := &v.Amenities[len(v.Amenities)-1]
		g.Items = append(g.Items, pages.AmenityItem{Label: a.Label, Icon: a.Icon})
	}

	lc := pages.ListerCard{Name: lister.Name, Kind: "Owner", PhoneVerified: lister.PhoneVerifiedAt != nil,
		IDVerified: lister.IdentityVerifiedAt != nil, Since: "On RentMap since " + lister.CreatedAt.Format("January 2006")}
	if lp := lister.Edges.LandlordProfile; lp != nil && lp.DisplayName != "" && d.L.ListerKind == listing.ListerKindOwner {
		lc.Name = lp.DisplayName
	}
	if d.L.ListerKind == listing.ListerKindAgent {
		lc.Kind, lc.IsAgent = "Agent", true
		lc.LicenceVerified = lister.LicenseVerifiedAt != nil
		if ap := lister.Edges.AgentProfile; ap != nil {
			lc.Agency = ap.AgencyName
		}
		lc.Authority, lc.AuthorityLabel = string(authority), mandates.Label(authority)
	}
	if lc.Name == "" {
		lc.Name = "RentMap " + strings.ToLower(lc.Kind)
	}
	lc.Initial = strings.ToUpper(string([]rune(lc.Name)[:1]))
	v.Lister = lc
	return v
}

// shareURL is a WhatsApp share link with the title, price and page URL.
func shareURL(v pages.ListingPageView, abs string) string {
	text := v.Headline
	if v.Price != "" {
		text += " — " + v.Price + " per " + v.Per
	}
	text += " in " + v.Area + "\n" + abs
	return "https://wa.me/?text=" + url.QueryEscape(text)
}
