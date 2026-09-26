package listings

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/platform/money"
	"rentmapgh/internal/platform/storage"
)

func setup(t *testing.T) (*Service, *ent.Client) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	require.NoError(t, db.Migrate(dsn))
	d, err := db.Open(ctx, dsn, 4)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	_, err = d.SQL.ExecContext(ctx, "TRUNCATE listing_media, listing_terms, listings, units, properties, audit_events, sessions, role_assignments, otp_codes, verification_files, verifications, agent_profiles, landlord_profiles, users")
	require.NoError(t, err)
	return NewService(d.Ent, audit.New(d.Ent), "test-location-secret", storage.NewMemory()), d.Ent
}

func lister(t *testing.T, c *ent.Client, verified bool, roles ...string) Actor {
	u, err := c.User.Create().SetPhone("+23324" + uuid.NewString()[:7]).Save(context.Background())
	require.NoError(t, err)
	return Actor{UserID: u.ID, Roles: roles, IdentityVerified: verified}
}

var fullSteps = []struct {
	step string
	form url.Values
}{
	{"location", url.Values{"lat": {"6.66970"}, "lng": {"-1.55880"}, "landmark": {"Behind the Ayeduase police station"}, "digital_address": {"ak0395028"}}},
	{"property", url.Values{"category": {"residential"}, "name": {""}}},
	{"unit", url.Values{"unit_type": {"chamber_hall_sc"}, "furnished": {"none"}, "self_contained": {"yes"},
		"meter_type": {"prepaid_own"}, "water_source": {"poly_tank"}, "kitchen": {"private"}, "bathrooms": {"1"}}},
	{"amenities", url.Values{"amenities": {"gated", "water_storage", "wifi", "not-a-thing"}}},
	{"photos", url.Values{}}, // fill adds the photos first
	{"pricing", url.Values{"rent": {"1,200"}, "rent_period": {"month"}, "advance_periods": {"6"}, "deposit": {"0"},
		"agent_fee": {"600"}, "service_charge": {"250"}, "viewing_fee": {"0"}}},
	{"details", url.Values{"headline": {"Self-contained chamber & hall near KNUST"}, "description": {"Quiet compound."}}},
}

func fill(t *testing.T, s *Service, a Actor, id uuid.UUID) *Item {
	var d *Item
	for _, st := range fullSteps {
		if st.step == "photos" {
			addPhotos(t, s, a, id, MinPhotos)
		}
		var errs ValidationError
		var err error
		d, errs, err = s.SaveStep(context.Background(), a, id, st.step, st.form, true)
		require.NoError(t, err, st.step)
		require.Nil(t, errs, st.step)
	}
	return d
}

func TestWizardHappyPathVerifiedGoesLive(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a := lister(t, c, true, "landlord")

	l, err := s.CreateDraft(ctx, a)
	require.NoError(t, err)
	assert.Equal(t, "location", l.WizardStep)
	assert.Equal(t, "owner", string(l.ListerKind))

	d := fill(t, s, a, l.ID)
	assert.Equal(t, "review", d.L.WizardStep, "furthest step remembered")
	assert.Equal(t, "AK-039-5028", d.P.DigitalAddress)
	assert.Equal(t, "ayeduase", d.P.Neighbourhood, "filled from the pin")
	assert.Equal(t, []string{"gated", "water_storage", "wifi"}, d.U.Amenities, "unknown keys dropped")
	assert.Equal(t, 1, *d.U.Bedrooms, "implied by the unit type")
	require.NotNil(t, d.T.MoveInTotal)
	assert.Equal(t, money.Pesewas(805000), *d.T.MoveInTotal)
	assert.Equal(t, money.Pesewas(120000), *d.T.MonthlyEquivalent)
	assert.Empty(t, Missing(d))
	assert.Greater(t, d.L.QualityScore, 40)

	st, err := s.Submit(ctx, a, l.ID)
	require.NoError(t, err)
	assert.Equal(t, Active, st)
	got, _ := s.Load(ctx, a, l.ID)
	assert.NotNil(t, got.L.PublishedAt)
	assert.NotNil(t, got.L.LastConfirmedAt)
}

func TestApproximateLocationIsStoredAndStable(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a := lister(t, c, false, "landlord")
	l, _ := s.CreateDraft(ctx, a)

	d, _, err := s.SaveStep(ctx, a, l.ID, "location", url.Values{"lat": {"6.66970"}, "lng": {"-1.55880"}}, false)
	require.NoError(t, err)
	exact := geo.Point{Lat: *d.P.Lat, Lng: *d.P.Lng}
	approx := geo.Point{Lat: *d.P.ApproxLat, Lng: *d.P.ApproxLng}
	dist := geo.Distance(exact, approx)
	assert.True(t, dist >= 148 && dist <= 402, "offset %.0f m", dist)

	// Nudging the pin 20 m keeps the public point exactly where it was.
	nudged := geo.Offset(exact, 20, 0.7)
	d, _, err = s.SaveStep(ctx, a, l.ID, "location", url.Values{"lat": {ftoa(nudged.Lat)}, "lng": {ftoa(nudged.Lng)}}, false)
	require.NoError(t, err)
	assert.Equal(t, approx, geo.Point{Lat: *d.P.ApproxLat, Lng: *d.P.ApproxLng})

	// Moving it 300 m recomputes.
	moved := geo.Offset(exact, 300, 2)
	d, _, err = s.SaveStep(ctx, a, l.ID, "location", url.Values{"lat": {ftoa(moved.Lat)}, "lng": {ftoa(moved.Lng)}}, false)
	require.NoError(t, err)
	assert.NotEqual(t, approx, geo.Point{Lat: *d.P.ApproxLat, Lng: *d.P.ApproxLng})

	_, errs, err := s.SaveStep(ctx, a, l.ID, "location", url.Values{"lat": {"51.5"}, "lng": {"-0.12"}}, true)
	require.NoError(t, err)
	assert.Contains(t, errs["pin"], "isn't in Ghana")
}

func TestUnverifiedListerGoesToReviewAndIsModerated(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a := lister(t, c, false, "agent")
	mod := lister(t, c, true, "moderator")
	l, err := s.CreateDraft(ctx, a)
	require.NoError(t, err)
	assert.Equal(t, "agent", string(l.ListerKind))
	fill(t, s, a, l.ID)

	st, err := s.Submit(ctx, a, l.ID)
	require.NoError(t, err)
	assert.Equal(t, PendingReview, st)
	q, err := s.Queue(ctx)
	require.NoError(t, err)
	require.Len(t, q, 1)
	assert.Equal(t, 1, s.PendingCount(ctx))

	// Sending back needs a note; it returns to draft with the note.
	var verr ValidationError
	require.ErrorAs(t, s.Decide(ctx, mod, l.ID, false, ""), &verr)
	require.NoError(t, s.Decide(ctx, mod, l.ID, false, "Please add the service charge details."))
	got, _ := s.Load(ctx, a, l.ID)
	assert.Equal(t, "draft", string(got.L.Status))
	assert.Equal(t, "Please add the service charge details.", got.L.ReviewNote)

	// Resubmit, approve → live.
	_, err = s.Submit(ctx, a, l.ID)
	require.NoError(t, err)
	require.ErrorAs(t, s.Decide(ctx, a, l.ID, true, ""), &verr, "no self-review")
	require.NoError(t, s.Decide(ctx, mod, l.ID, true, ""))
	got, _ = s.Load(ctx, a, l.ID)
	assert.Equal(t, "active", string(got.L.Status))
	require.ErrorAs(t, s.Decide(ctx, mod, l.ID, true, ""), &verr, "already decided")
}

func TestIncompleteListingCantBePublished(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a := lister(t, c, true, "landlord")
	l, _ := s.CreateDraft(ctx, a)

	// Pricing with fees left blank: autosave keeps what's there…
	d, errs, err := s.SaveStep(ctx, a, l.ID, "pricing", url.Values{"rent": {"900"}, "rent_period": {"month"}, "advance_periods": {"12"},
		"deposit": {""}, "agent_fee": {""}, "service_charge": {""}, "viewing_fee": {""}}, false)
	require.NoError(t, err)
	assert.Nil(t, errs)
	assert.Equal(t, money.Pesewas(90000), *d.T.Rent)
	// …but Continue insists on every fee.
	_, errs, err = s.SaveStep(ctx, a, l.ID, "pricing", url.Values{"rent": {"900"}, "rent_period": {"month"}, "advance_periods": {"12"},
		"deposit": {""}, "agent_fee": {"0"}, "service_charge": {""}, "viewing_fee": {"0"}}, true)
	require.NoError(t, err)
	assert.Contains(t, errs, "deposit")
	assert.Contains(t, errs, "service_charge")
	assert.NotContains(t, errs, "agent_fee", "0 is an answer")

	d, _ = s.Load(ctx, a, l.ID)
	missing := Missing(d)
	steps := map[string]bool{}
	for _, m := range missing {
		steps[m.Step] = true
	}
	assert.True(t, steps["location"] && steps["unit"] && steps["photos"] && steps["pricing"] && steps["details"], missing)
	_, err = s.Submit(ctx, a, l.ID)
	assert.ErrorIs(t, err, ErrIncomplete)
}

func TestListingsAreIsolatedByLister(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	alice := lister(t, c, true, "landlord")
	bob := lister(t, c, true, "landlord")
	renter := lister(t, c, false, "renter")

	l, _ := s.CreateDraft(ctx, alice)
	_, err := s.Load(ctx, bob, l.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.SaveStep(ctx, bob, l.ID, "details", url.Values{"headline": {"Hijacked listing title"}}, false)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.Submit(ctx, bob, l.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.AddUnit(ctx, bob, l.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.CreateDraft(ctx, renter)
	assert.ErrorIs(t, err, ErrNotLister)

	mine, err := s.Mine(ctx, bob)
	require.NoError(t, err)
	assert.Empty(t, mine)
}

func TestAddUnitSharesTheProperty(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a := lister(t, c, true, "landlord")
	l, _ := s.CreateDraft(ctx, a)
	first := fill(t, s, a, l.ID)

	l2, err := s.AddUnit(ctx, a, l.ID)
	require.NoError(t, err)
	assert.Equal(t, "unit", l2.WizardStep, "location and property are already done")
	second, err := s.Load(ctx, a, l2.ID)
	require.NoError(t, err)
	assert.Equal(t, first.P.ID, second.P.ID)
	assert.NotEqual(t, first.U.ID, second.U.ID)
	assert.Equal(t, first.P.Lat, second.P.Lat)
}

func TestListerActions(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a := lister(t, c, true, "landlord")
	l, _ := s.CreateDraft(ctx, a)
	fill(t, s, a, l.ID)
	_, err := s.Act(ctx, a, l.ID, EvPause)
	assert.ErrorIs(t, err, ErrTransition, "draft can't be paused")
	_, err = s.Submit(ctx, a, l.ID)
	require.NoError(t, err)
	for _, step := range []struct {
		ev   Event
		want Status
	}{{EvPause, Paused}, {EvResume, Active}, {EvMarkRented, Rented}, {EvRelist, Active}} {
		st, err := s.Act(ctx, a, l.ID, step.ev)
		require.NoError(t, err, step.ev)
		assert.Equal(t, step.want, st)
	}
	_, err = s.Act(ctx, a, l.ID, EvApprove)
	assert.ErrorIs(t, err, ErrTransition, "listers can't approve")
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }
