package listings

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/platform/money"
	"rentmapgh/internal/platform/storage"
	"rentmapgh/internal/platform/video"
)

// Wizard steps, in order.
var Steps = []StepInfo{
	{"location", "Location", "Pin and address"},
	{"property", "Property", "Building type"},
	{"unit", "The space", "Rooms and utilities"},
	{"amenities", "Amenities", "What's included"},
	{"photos", "Photos", "Show the place"},
	{"pricing", "Pricing", "Rent and every fee"},
	{"details", "Details", "Headline and description"},
	{"review", "Review", "Check and publish"},
}

type StepInfo struct{ Key, Label, Hint string }

func stepIndex(key string) int {
	for i, s := range Steps {
		if s.Key == key {
			return i
		}
	}
	return -1
}

// NextStep returns the step after key ("" after review).
func NextStep(key string) string {
	if i := stepIndex(key); i >= 0 && i+1 < len(Steps) {
		return Steps[i+1].Key
	}
	return ""
}

var (
	ErrNotFound   = errors.New("listings: not found")
	ErrNotLister  = errors.New("Add the landlord or agent role to your account to list a property.")
	ErrIncomplete = errors.New("Some required details are missing.")
)

// ValidationError maps form field → message.
type ValidationError map[string]string

func (v ValidationError) Error() string { return fmt.Sprintf("listings: %d invalid field(s)", len(v)) }

// Actor is the signed-in lister.
type Actor struct {
	UserID           uuid.UUID
	Roles            []string
	IdentityVerified bool
	IP, UserAgent    string
}

func (a Actor) isLister() bool {
	return slices.Contains(a.Roles, "landlord") || slices.Contains(a.Roles, "agent")
}

type Service struct {
	db        *ent.Client
	audit     *audit.Log
	locSecret []byte
	media     storage.Store // public listing photos
	imaging   chan struct{} // bounds concurrent photo decoding (memory)
	video     *video.Tool   // nil: walk-through videos are off
	inbox     string        // local dir where video uploads are assembled
	wake      chan struct{} // nudges the video worker
	now       func() time.Time
}

func NewService(db *ent.Client, log *audit.Log, locationSecret string, media storage.Store) *Service {
	return &Service{db: db, audit: log, locSecret: []byte(locationSecret), media: media,
		imaging: make(chan struct{}, 2), wake: make(chan struct{}, 1), now: func() time.Time { return time.Now().UTC() }}
}

// Item is a listing with its unit, property, terms and media (in order).
type Item struct {
	L *ent.Listing
	U *ent.Unit
	P *ent.Property
	T *ent.ListingTerms
	M []*ent.ListingMedia
}

// Terms converts stored terms for ComputeMoveIn.
func (d *Item) Terms() Terms {
	t := d.T
	if t == nil {
		return Terms{Period: "month"}
	}
	return Terms{Rent: t.Rent, Period: t.RentPeriod, AdvancePeriods: t.AdvancePeriods, Deposit: t.Deposit,
		AgentFee: t.AgentFee, ServiceCharge: t.ServiceCharge, ViewingFee: t.ViewingFee, OtherFees: t.OtherFees}
}

// ── Creating ──────────────────────────────────────────────────────────────

// CreateDraft starts a new property + unit + listing for the lister.
func (s *Service) CreateDraft(ctx context.Context, a Actor) (*ent.Listing, error) {
	if !a.isLister() {
		return nil, ErrNotLister
	}
	kind := listing.ListerKindOwner
	if !slices.Contains(a.Roles, "landlord") {
		kind = listing.ListerKindAgent
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("create draft: begin: %w", err)
	}
	pc := tx.Property.Create().SetCreatedBy(a.UserID)
	if kind == listing.ListerKindOwner {
		pc.SetOwnerID(a.UserID)
	}
	p, err := pc.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("create draft: property: %w", err)
	}
	l, err := s.newListing(ctx, tx, a, p.ID, kind, "location")
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("create draft: commit: %w", err)
	}
	return l, nil
}

// AddUnit adds another unit + draft listing to the property of an existing
// listing (hostels, flats in a block, rooms in a compound house).
func (s *Service) AddUnit(ctx context.Context, a Actor, fromListing uuid.UUID) (*ent.Listing, error) {
	d, err := s.Load(ctx, a, fromListing)
	if err != nil {
		return nil, err
	}
	if d.P.CreatedBy != a.UserID {
		return nil, ErrNotFound
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("add unit: begin: %w", err)
	}
	l, err := s.newListing(ctx, tx, a, d.P.ID, d.L.ListerKind, "unit")
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("add unit: commit: %w", err)
	}
	return l, nil
}

func (s *Service) newListing(ctx context.Context, tx *ent.Tx, a Actor, propertyID uuid.UUID, kind listing.ListerKind, step string) (*ent.Listing, error) {
	u, err := tx.Unit.Create().SetPropertyID(propertyID).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("new listing: unit: %w", err)
	}
	l, err := tx.Listing.Create().SetUnitID(u.ID).SetListerID(a.UserID).SetListerKind(kind).SetWizardStep(step).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("new listing: listing: %w", err)
	}
	if err := tx.ListingTerms.Create().SetListingID(l.ID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("new listing: terms: %w", err)
	}
	if err := audit.RecordTx(ctx, tx, audit.Event{Actor: &a.UserID, Action: "listing.created", TargetType: "listing",
		TargetID: l.ID.String(), IP: a.IP, UserAgent: a.UserAgent}); err != nil {
		return nil, fmt.Errorf("new listing: audit: %w", err)
	}
	return l, nil
}

// ── Loading ───────────────────────────────────────────────────────────────

// Load returns a listing the actor lists. Anyone else gets ErrNotFound (not
// "forbidden", so IDs can't be probed).
func (s *Service) Load(ctx context.Context, a Actor, id uuid.UUID) (*Item, error) {
	l, err := s.db.Listing.Query().Where(listing.ID(id), listing.ListerID(a.UserID)).
		WithTerms().WithUnit(func(q *ent.UnitQuery) { q.WithProperty() }).WithMedia(withMedia).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load listing: %w", err)
	}
	return draftOf(l), nil
}

func draftOf(l *ent.Listing) *Item {
	d := &Item{L: l, T: l.Edges.Terms, U: l.Edges.Unit, M: l.Edges.Media}
	if d.U != nil {
		d.P = d.U.Edges.Property
	}
	return d
}

// Mine lists the actor's listings, newest first.
func (s *Service) Mine(ctx context.Context, a Actor) ([]*Item, error) {
	ls, err := s.db.Listing.Query().Where(listing.ListerID(a.UserID), listing.StatusNEQ(listing.StatusRemoved)).
		WithTerms().WithUnit(func(q *ent.UnitQuery) { q.WithProperty() }).WithMedia(withMedia).
		Order(ent.Desc(listing.FieldUpdatedAt)).Limit(200).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("my listings: %w", err)
	}
	out := make([]*Item, len(ls))
	for i, l := range ls {
		out[i] = draftOf(l)
	}
	return out, nil
}

// ── Saving wizard steps ───────────────────────────────────────────────────

// SaveStep stores the fields of one wizard step. With strict, missing
// required fields are reported (the "Continue" button); without, whatever
// is valid is saved and the rest ignored (autosave while typing).
func (s *Service) SaveStep(ctx context.Context, a Actor, id uuid.UUID, step string, f url.Values, strict bool) (*Item, ValidationError, error) {
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return nil, nil, err
	}
	if !Editable(Status(d.L.Status)) {
		return d, ValidationError{"form": "This listing can't be edited."}, nil
	}
	errs := ValidationError{}
	switch step {
	case "location":
		err = s.saveLocation(ctx, d, f, strict, errs)
	case "property":
		err = s.saveProperty(ctx, d, f, strict, errs)
	case "unit":
		err = s.saveUnit(ctx, d, f, strict, errs)
	case "amenities":
		err = s.saveAmenities(ctx, d, f)
	case "photos": // files arrive through AddPhoto; Continue only checks the count
		if strict && len(d.Photos()) < MinPhotos && f.Get("later") != "1" {
			errs["photos"] = fmt.Sprintf("Add at least %d photos, or choose “Add photos later”.", MinPhotos)
		}
	case "pricing":
		err = s.savePricing(ctx, d, f, strict, errs)
	case "details":
		err = s.saveDetails(ctx, d, f, strict, errs)
	case "review":
	default:
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if len(errs) == 0 && strict {
		if next := NextStep(step); next != "" && stepIndex(next) > stepIndex(d.L.WizardStep) {
			d.L.WizardStep = next
		}
	}
	if err := s.refresh(ctx, d); err != nil {
		return nil, nil, err
	}
	if len(errs) == 0 {
		errs = nil
	}
	return d, errs, nil
}

// refresh reloads the draft and recomputes derived fields (quality score,
// move-in total, monthly equivalent).
func (s *Service) refresh(ctx context.Context, d *Item) error {
	fresh, err := s.db.Listing.Query().Where(listing.ID(d.L.ID)).
		WithTerms().WithUnit(func(q *ent.UnitQuery) { q.WithProperty() }).WithMedia(withMedia).Only(ctx)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	step := d.L.WizardStep
	*d = *draftOf(fresh)
	score, _ := Quality(QualityOf(d))
	m := ComputeMoveIn(d.Terms())
	up := s.db.ListingTerms.UpdateOneID(d.T.ID).ClearMonthlyEquivalent().ClearMoveInTotal()
	if d.T.Rent != nil {
		up.SetMonthlyEquivalent(m.Monthly).SetMoveInTotal(m.Total)
	}
	if err := up.Exec(ctx); err != nil {
		return fmt.Errorf("refresh: terms: %w", err)
	}
	if d.L, err = s.db.Listing.UpdateOneID(d.L.ID).SetQualityScore(score).SetWizardStep(step).Save(ctx); err != nil {
		return fmt.Errorf("refresh: listing: %w", err)
	}
	d.T.MonthlyEquivalent, d.T.MoveInTotal = &m.Monthly, &m.Total
	return nil
}

// QualityOf reads the quality inputs from a draft.
func QualityOf(d *Item) QualityInput {
	q := QualityInput{
		HeadlineLen:      runeLen(d.L.Headline),
		DescriptionLen:   runeLen(d.L.Description),
		HasAvailableFrom: d.L.AvailableFrom != nil,
		FeesComplete:     ComputeMoveIn(d.Terms()).Complete,
		Photos:           len(d.Photos()),
		HasVideo:         d.HasReadyVideo(),
	}
	if d.P != nil {
		q.HasLocation = d.P.Lat != nil
		q.HasLandmark = d.P.Landmark != ""
		q.HasDigitalAddress = d.P.DigitalAddress != ""
	}
	if d.U != nil {
		q.Amenities = len(d.U.Amenities)
	}
	return q
}

func (s *Service) saveLocation(ctx context.Context, d *Item, f url.Values, strict bool, errs ValidationError) error {
	up := s.db.Property.UpdateOneID(d.P.ID)
	lat, errLat := strconv.ParseFloat(strings.TrimSpace(f.Get("lat")), 64)
	lng, errLng := strconv.ParseFloat(strings.TrimSpace(f.Get("lng")), 64)
	pinned := errLat == nil && errLng == nil
	pt := geo.Point{Lat: lat, Lng: lng}
	switch {
	case pinned && !geo.InGhana(pt):
		errs["pin"] = "That pin isn't in Ghana. Drag it to the property."
	case pinned:
		up.SetLat(lat).SetLng(lng)
		if d.P.Lat == nil || d.P.ApproxLat == nil || geo.ShouldRecompute(geo.Point{Lat: *d.P.Lat, Lng: *d.P.Lng}, pt) {
			ap := geo.Approximate(s.locSecret, d.P.ID, pt)
			up.SetApproxLat(ap.Lat).SetApproxLng(ap.Lng)
		}
		if f.Get("neighbourhood") == "" && d.P.Neighbourhood == "" {
			up.SetNeighbourhood(geo.Nearest(pt).Slug)
		}
	case strict && d.P.Lat == nil:
		errs["pin"] = "Drop the pin on the property, or use your current location."
	}

	if v := strings.TrimSpace(f.Get("digital_address")); v != "" {
		if da := geo.NormalizeDigitalAddress(v); da != "" {
			up.SetDigitalAddress(da)
		} else {
			errs["digital_address"] = "That isn't a GhanaPostGPS address. It looks like AK-039-5028."
		}
	} else if f.Has("digital_address") {
		up.SetDigitalAddress("")
	}
	if f.Has("landmark") {
		if v := clean(f.Get("landmark")); runeLen(v) > 160 {
			errs["landmark"] = "Keep the landmark under 160 characters."
		} else {
			up.SetLandmark(v)
		}
	}
	if f.Has("street") {
		if v := clean(f.Get("street")); runeLen(v) > 120 {
			errs["street"] = "Keep the street under 120 characters."
		} else {
			up.SetStreet(v)
		}
	}
	if v := f.Get("neighbourhood"); v != "" {
		if _, ok := geo.NeighbourhoodBySlug(v); ok {
			up.SetNeighbourhood(v)
		} else {
			errs["neighbourhood"] = "Choose an area from the list."
		}
	}
	return up.Exec(ctx)
}

func (s *Service) saveProperty(ctx context.Context, d *Item, f url.Values, strict bool, errs ValidationError) error {
	up := s.db.Property.UpdateOneID(d.P.ID)
	if v := f.Get("category"); v != "" {
		if optionValid(Categories, v) {
			up.SetCategory(v)
		} else {
			errs["category"] = "Choose the kind of property."
		}
	}
	if f.Has("name") {
		if v := clean(f.Get("name")); runeLen(v) > 80 {
			errs["name"] = "Keep the name under 80 characters."
		} else {
			up.SetName(v)
		}
	}
	_ = strict // category always has a default
	return up.Exec(ctx)
}

func (s *Service) saveUnit(ctx context.Context, d *Item, f url.Values, strict bool, errs ValidationError) error {
	up := s.db.Unit.UpdateOneID(d.U.ID)
	ut, typed := unitType(f.Get("unit_type"))
	switch {
	case f.Get("unit_type") != "" && !typed:
		errs["unit_type"] = "Choose the type of space."
	case typed:
		up.SetUnitType(ut.Key)
		if ut.Bedrooms >= 0 {
			up.SetBedrooms(ut.Bedrooms)
		}
	case strict && d.U.UnitType == "":
		errs["unit_type"] = "Choose the type of space."
	}
	if !typed {
		ut, typed = unitType(d.U.UnitType)
	}

	intField(f, "bedrooms", 0, 20, errs, func(v *int) {
		if v != nil {
			up.SetBedrooms(*v)
		}
	})
	if strict && typed && ut.Bedrooms < 0 && f.Get("bedrooms") == "" && d.U.Bedrooms == nil {
		errs["bedrooms"] = "How many bedrooms?"
	}
	intField(f, "bathrooms", 0, 20, errs, func(v *int) {
		if v == nil {
			up.ClearBathrooms()
		} else {
			up.SetBathrooms(*v)
		}
	})
	intField(f, "size_sqm", 1, 5000, errs, func(v *int) {
		if v == nil {
			up.ClearSizeSqm()
		} else {
			up.SetSizeSqm(*v)
		}
	})
	intField(f, "floor", -2, 60, errs, func(v *int) {
		if v == nil {
			up.ClearFloor()
		} else {
			up.SetFloor(*v)
		}
	})
	if f.Has("label") {
		if v := clean(f.Get("label")); runeLen(v) > 40 {
			errs["label"] = "Keep it under 40 characters."
		} else {
			up.SetLabel(v)
		}
	}
	residential := !typed || ut.Residential
	choice := func(name string, opts []Option, current string, set func(string)) {
		v := f.Get(name)
		switch {
		case v != "" && optionValid(opts, v):
			set(v)
		case v != "":
			errs[name] = "Choose one of the options."
		case strict && residential && current == "":
			errs[name] = "Please choose one."
		}
	}
	choice("furnished", FurnishedOptions, d.U.Furnished, func(v string) { up.SetFurnished(v) })
	choice("meter_type", MeterOptions, d.U.MeterType, func(v string) { up.SetMeterType(v) })
	choice("water_source", WaterOptions, d.U.WaterSource, func(v string) { up.SetWaterSource(v) })
	choice("kitchen", KitchenOptions, d.U.Kitchen, func(v string) { up.SetKitchen(v) })
	switch f.Get("self_contained") {
	case "yes":
		up.SetSelfContained(true)
	case "no":
		up.SetSelfContained(false)
	case "":
		if strict && residential && d.U.SelfContained == nil {
			errs["self_contained"] = "Does it have its own bathroom and toilet?"
		}
	}
	return up.Exec(ctx)
}

func (s *Service) saveAmenities(ctx context.Context, d *Item, f url.Values) error {
	var keys []string
	for _, k := range f["amenities"] {
		if isAmenity(k) && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return s.db.Unit.UpdateOneID(d.U.ID).SetAmenities(keys).Exec(ctx)
}

func (s *Service) savePricing(ctx context.Context, d *Item, f url.Values, strict bool, errs ValidationError) error {
	up := s.db.ListingTerms.UpdateOneID(d.T.ID)
	if v := f.Get("rent_period"); v != "" {
		if _, ok := rentPeriod(v); ok {
			up.SetRentPeriod(v)
		} else {
			errs["rent_period"] = "Choose how often rent is paid."
		}
	}
	amount := func(name string, required bool, set func(*money.Pesewas)) {
		if !f.Has(name) {
			return
		}
		raw := strings.TrimSpace(f.Get(name))
		if raw == "" {
			set(nil)
			if strict && required {
				errs[name] = "Enter an amount, or 0 if there isn't one."
			}
			return
		}
		p, err := money.Parse(raw)
		if err != nil {
			errs[name] = err.Error()
			return
		}
		set(&p)
	}
	nillable := func(clear func(), set func(money.Pesewas)) func(*money.Pesewas) {
		return func(p *money.Pesewas) {
			if p == nil {
				clear()
			} else {
				set(*p)
			}
		}
	}
	amount("rent", true, nillable(func() { up.ClearRent() }, func(p money.Pesewas) { up.SetRent(p) }))
	if strict && errs["rent"] == "" {
		if r, err := money.Parse(f.Get("rent")); err == nil && r == 0 {
			errs["rent"] = "Rent can't be zero."
		}
	}
	amount("deposit", true, nillable(func() { up.ClearDeposit() }, func(p money.Pesewas) { up.SetDeposit(p) }))
	amount("agent_fee", true, nillable(func() { up.ClearAgentFee() }, func(p money.Pesewas) { up.SetAgentFee(p) }))
	amount("service_charge", true, nillable(func() { up.ClearServiceCharge() }, func(p money.Pesewas) { up.SetServiceCharge(p) }))
	amount("viewing_fee", true, nillable(func() { up.ClearViewingFee() }, func(p money.Pesewas) { up.SetViewingFee(p) }))

	intField(f, "advance_periods", 1, 60, errs, func(v *int) {
		if v == nil {
			up.ClearAdvancePeriods()
		} else {
			up.SetAdvancePeriods(*v)
		}
	})
	if strict && strings.TrimSpace(f.Get("advance_periods")) == "" {
		errs["advance_periods"] = "How much rent is paid before moving in?"
	}
	intField(f, "min_lease_months", 1, 120, errs, func(v *int) {
		if v == nil {
			up.ClearMinLeaseMonths()
		} else {
			up.SetMinLeaseMonths(*v)
		}
	})
	if f.Has("rent") { // the pricing form was posted: checkboxes are authoritative
		up.SetNegotiable(f.Get("negotiable") == "1")
	}

	var fees []money.Fee
	for i := 1; i <= 3; i++ {
		label := clean(f.Get("fee_label_" + strconv.Itoa(i)))
		raw := strings.TrimSpace(f.Get("fee_amount_" + strconv.Itoa(i)))
		if label == "" && raw == "" {
			continue
		}
		key := "fee_" + strconv.Itoa(i)
		p, err := money.Parse(raw)
		switch {
		case label == "":
			errs[key] = "Name this fee."
		case runeLen(label) > 40:
			errs[key] = "Keep the fee name under 40 characters."
		case err != nil:
			errs[key] = err.Error()
		default:
			fees = append(fees, money.Fee{Label: label, Amount: p})
		}
	}
	if f.Has("rent") {
		up.SetOtherFees(fees)
	}
	return up.Exec(ctx)
}

func (s *Service) saveDetails(ctx context.Context, d *Item, f url.Values, strict bool, errs ValidationError) error {
	up := s.db.Listing.UpdateOneID(d.L.ID)
	if f.Has("headline") {
		v := clean(f.Get("headline"))
		switch {
		case runeLen(v) > 90:
			errs["headline"] = "Keep the headline under 90 characters."
		case strict && runeLen(v) < 10:
			errs["headline"] = "Write a headline of at least 10 characters."
		default:
			up.SetHeadline(v)
		}
	}
	if f.Has("description") {
		v := strings.TrimSpace(f.Get("description"))
		if runeLen(v) > 3000 {
			errs["description"] = "Keep the description under 3,000 characters."
		} else {
			up.SetDescription(v)
		}
	}
	if f.Has("available_from") {
		raw := strings.TrimSpace(f.Get("available_from"))
		if raw == "" {
			up.ClearAvailableFrom()
		} else if t, err := time.Parse("2006-01-02", raw); err != nil {
			errs["available_from"] = "Pick a date."
		} else if t.Before(s.now().AddDate(0, 0, -1).Truncate(24 * time.Hour)) {
			errs["available_from"] = "Pick today or a later date."
		} else {
			up.SetAvailableFrom(t)
		}
	}
	return up.Exec(ctx)
}

// ── Publishing ────────────────────────────────────────────────────────────

// Missing lists what still blocks publishing, as step → message.
func Missing(d *Item) []Requirement {
	var out []Requirement
	if d.P.Lat == nil {
		out = append(out, Requirement{"location", "Drop the pin on the property"})
	}
	if d.U.UnitType == "" {
		out = append(out, Requirement{"unit", "Choose the type of space"})
	} else if ut, _ := unitType(d.U.UnitType); ut.Residential {
		if d.U.Furnished == "" || d.U.MeterType == "" || d.U.WaterSource == "" || d.U.Kitchen == "" || d.U.SelfContained == nil {
			out = append(out, Requirement{"unit", "Finish the rooms and utilities questions"})
		}
		if d.U.Bedrooms == nil {
			out = append(out, Requirement{"unit", "Say how many bedrooms"})
		}
	}
	if n := len(d.Photos()); n < MinPhotos {
		out = append(out, Requirement{"photos", fmt.Sprintf("Add at least %d photos (%d so far)", MinPhotos, n)})
	}
	m := ComputeMoveIn(d.Terms())
	if !m.Complete {
		out = append(out, Requirement{"pricing", "Answer every fee (" + strings.Join(m.Missing, ", ") + ") — enter 0 if there isn't one"})
	} else if d.T.Rent != nil && *d.T.Rent == 0 {
		out = append(out, Requirement{"pricing", "Enter the rent"})
	}
	if runeLen(d.L.Headline) < 10 {
		out = append(out, Requirement{"details", "Write a headline"})
	}
	return out
}

type Requirement struct{ Step, Text string }

// Submit publishes a complete draft: live straight away for ID-verified
// listers, otherwise into the moderator queue.
func (s *Service) Submit(ctx context.Context, a Actor, id uuid.UUID) (Status, error) {
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return "", err
	}
	if len(Missing(d)) > 0 {
		return "", ErrIncomplete
	}
	ev := EvSubmitUnverified
	if a.IdentityVerified {
		ev = EvSubmitVerified
	}
	to, err := Next(Status(d.L.Status), ev)
	if err != nil {
		return "", err
	}
	now := s.now()
	up := s.db.Listing.UpdateOneID(id).SetStatus(listing.Status(to)).SetSubmittedAt(now).SetReviewNote("")
	if to == Active {
		up.SetPublishedAt(now).SetLastConfirmedAt(now)
	}
	if err := up.Exec(ctx); err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: "listing.submitted", TargetType: "listing", TargetID: id.String(),
		IP: a.IP, UserAgent: a.UserAgent, Meta: map[string]any{"status": string(to)}})
	return to, nil
}

// Act applies a lister action (pause, resume, mark_rented, relist, withdraw).
func (s *Service) Act(ctx context.Context, a Actor, id uuid.UUID, ev Event) (Status, error) {
	switch ev {
	case EvPause, EvResume, EvMarkRented, EvRelist, EvWithdraw, EvConfirm:
	default:
		return "", ErrTransition
	}
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return "", err
	}
	if ev == EvConfirm {
		if Status(d.L.Status) != Active {
			return "", ErrTransition
		}
		if err := s.db.Listing.UpdateOneID(id).SetLastConfirmedAt(s.now()).ClearStaleReportedAt().Exec(ctx); err != nil {
			return "", fmt.Errorf("act: confirm: %w", err)
		}
		s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: "listing.confirmed", TargetType: "listing", TargetID: id.String(), IP: a.IP, UserAgent: a.UserAgent})
		return Active, nil
	}
	to, err := Next(Status(d.L.Status), ev)
	if err != nil {
		return "", err
	}
	up := s.db.Listing.UpdateOneID(id).SetStatus(listing.Status(to))
	if to == Active { // resuming or relisting counts as confirming availability
		up.SetLastConfirmedAt(s.now()).ClearStaleReportedAt()
	}
	if err := up.Exec(ctx); err != nil {
		return "", fmt.Errorf("act: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: "listing." + string(ev), TargetType: "listing", TargetID: id.String(),
		IP: a.IP, UserAgent: a.UserAgent})
	return to, nil
}

// ── Moderation ────────────────────────────────────────────────────────────

// Queue lists listings waiting for review, oldest first.
func (s *Service) Queue(ctx context.Context) ([]*Item, error) {
	ls, err := s.db.Listing.Query().Where(listing.StatusEQ(listing.StatusPendingReview)).
		WithTerms().WithUnit(func(q *ent.UnitQuery) { q.WithProperty() }).WithMedia(withMedia).
		Order(ent.Asc(listing.FieldSubmittedAt)).Limit(100).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing queue: %w", err)
	}
	out := make([]*Item, len(ls))
	for i, l := range ls {
		out[i] = draftOf(l)
	}
	return out, nil
}

// PendingCount is the badge on the admin nav.
func (s *Service) PendingCount(ctx context.Context) int {
	n, _ := s.db.Listing.Query().Where(listing.StatusEQ(listing.StatusPendingReview)).Count(ctx)
	return n
}

// ForReview loads any listing for a moderator.
func (s *Service) ForReview(ctx context.Context, id uuid.UUID) (*Item, *ent.User, error) {
	l, err := s.db.Listing.Query().Where(listing.ID(id)).
		WithTerms().WithUnit(func(q *ent.UnitQuery) { q.WithProperty() }).WithMedia(withMedia).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("review: %w", err)
	}
	lister, err := s.db.User.Get(ctx, l.ListerID)
	if err != nil {
		return nil, nil, fmt.Errorf("review: lister: %w", err)
	}
	return draftOf(l), lister, nil
}

// Decide approves a pending listing or sends it back with a note.
func (s *Service) Decide(ctx context.Context, moderator Actor, id uuid.UUID, approve bool, note string) error {
	note = strings.TrimSpace(note)
	if !approve && note == "" {
		return ValidationError{"note": "Tell the lister what to change."}
	}
	if runeLen(note) > 500 {
		return ValidationError{"note": "Keep the note under 500 characters."}
	}
	l, err := s.db.Listing.Get(ctx, id)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if l.ListerID == moderator.UserID {
		return ValidationError{"form": "You can't review your own listing."}
	}
	ev := EvApprove
	if !approve {
		ev = EvRequestChanges
	}
	to, err := Next(Status(l.Status), ev)
	if err != nil {
		return ValidationError{"form": "This listing has already been reviewed."}
	}
	now := s.now()
	up := s.db.Listing.Update().Where(listing.ID(id), listing.StatusEQ(listing.StatusPendingReview)).
		SetStatus(listing.Status(to)).SetReviewedBy(moderator.UserID).SetReviewNote(note)
	if to == Active {
		up.SetPublishedAt(now).SetLastConfirmedAt(now)
	}
	n, err := up.Save(ctx)
	if err != nil {
		return fmt.Errorf("decide: %w", err)
	}
	if n == 0 {
		return ValidationError{"form": "This listing has already been reviewed."}
	}
	s.audit.Record(ctx, audit.Event{Actor: &moderator.UserID, Action: "listing." + string(ev), TargetType: "listing",
		TargetID: id.String(), IP: moderator.IP, UserAgent: moderator.UserAgent})
	return nil
}

// Remove takes a listing down (moderator): it leaves search and its page
// for good. The reason is kept as the review note.
func (s *Service) Remove(ctx context.Context, moderator Actor, id uuid.UUID, reason string) (*ent.Listing, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, ValidationError{"reason": "Say why it's coming down."}
	}
	if runeLen(reason) > 500 {
		return nil, ValidationError{"reason": "Keep the reason under 500 characters."}
	}
	l, err := s.db.Listing.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if l.ListerID == moderator.UserID {
		return nil, ValidationError{"reason": "You can't moderate your own listing."}
	}
	if _, err := Next(Status(l.Status), EvRemove); err != nil {
		return nil, ValidationError{"reason": "This listing is already down."}
	}
	l, err = s.db.Listing.UpdateOne(l).SetStatus(listing.StatusRemoved).SetReviewedBy(moderator.UserID).SetReviewNote(reason).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("remove: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &moderator.UserID, Action: "listing.removed", TargetType: "listing", TargetID: id.String(),
		IP: moderator.IP, UserAgent: moderator.UserAgent, Meta: map[string]any{"reason": reason}})
	return l, nil
}

// ── helpers ───────────────────────────────────────────────────────────────

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// intField parses an optional integer form field within [lo, hi] and calls
// set with nil when the field was posted empty.
func intField(f url.Values, name string, lo, hi int, errs ValidationError, set func(*int)) {
	if !f.Has(name) {
		return
	}
	raw := strings.TrimSpace(f.Get(name))
	if raw == "" {
		set(nil)
		return
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		errs[name] = fmt.Sprintf("Enter a whole number from %d to %d.", lo, hi)
		return
	}
	set(&n)
}
