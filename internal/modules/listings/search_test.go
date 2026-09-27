package listings

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/platform/money"
)

type spot struct {
	lat, lng         float64
	unit, sc, period string
	rent             int
	amenities        []string
}

// live creates and publishes one listing with the given place and terms.
func live(t *testing.T, s *Service, a Actor, sp spot) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	l, err := s.CreateDraft(ctx, a)
	require.NoError(t, err)
	steps := []struct {
		step string
		form url.Values
	}{
		{"location", url.Values{"lat": {strconv.FormatFloat(sp.lat, 'f', 5, 64)}, "lng": {strconv.FormatFloat(sp.lng, 'f', 5, 64)}}},
		{"property", url.Values{"category": {"residential"}}},
		{"unit", url.Values{"unit_type": {sp.unit}, "furnished": {"none"}, "self_contained": {sp.sc},
			"meter_type": {"prepaid_own"}, "water_source": {"borehole"}, "kitchen": {"private"}, "bathrooms": {"1"}}},
		{"amenities", url.Values{"amenities": sp.amenities}},
		{"photos", url.Values{}},
		{"pricing", url.Values{"rent": {strconv.Itoa(sp.rent)}, "rent_period": {sp.period}, "advance_periods": {"1"}, "deposit": {"0"},
			"agent_fee": {"0"}, "service_charge": {"0"}, "viewing_fee": {"0"}}},
		{"details", url.Values{"headline": {"A place to rent near campus"}, "description": {"Nice."}}},
	}
	for _, st := range steps {
		if st.step == "photos" {
			addPhotos(t, s, a, l.ID, MinPhotos)
		}
		_, errs, err := s.SaveStep(ctx, a, l.ID, st.step, st.form, true)
		require.NoError(t, err, st.step)
		require.Nil(t, errs, st.step)
	}
	_, err = s.Submit(ctx, a, l.ID)
	require.NoError(t, err)
	return l.ID
}

func ids(items []*Item) []uuid.UUID {
	var out []uuid.UUID
	for _, it := range items {
		out = append(out, it.L.ID)
	}
	return out
}

func TestSearch(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a := lister(t, c, true, "landlord")

	cheap := live(t, s, a, spot{6.6697, -1.5588, "single_room", "no", "month", 400, []string{"water_storage"}})
	mid := live(t, s, a, spot{6.6840, -1.5640, "chamber_hall_sc", "yes", "month", 1200, []string{"gated", "parking"}})
	hostel := live(t, s, a, spot{6.6630, -1.5530, "hostel_2in1", "yes", "academic_year", 4000, []string{"wifi", "cctv"}}) // 500/month
	far := live(t, s, a, spot{6.6930, -1.6240, "apartment_2bed", "yes", "month", 3000, []string{"parking"}})              // Adum, outside the default view

	// A draft never shows.
	draft, err := s.CreateDraft(ctx, a)
	require.NoError(t, err)

	search := func(q string) []uuid.UUID {
		v, err := url.ParseQuery(q)
		require.NoError(t, err)
		res, err := s.Search(ctx, ParseFilter(v))
		require.NoError(t, err)
		assert.Len(t, res.Items, min(res.Total, PageSize), q)
		return ids(res.Items)
	}

	all := search("")
	assert.ElementsMatch(t, []uuid.UUID{cheap, mid, hostel}, all, "default view is the KNUST belt")
	assert.NotContains(t, all, draft.ID)
	assert.ElementsMatch(t, []uuid.UUID{cheap, mid, hostel, far}, search("bbox=-1.70,6.60,-1.50,6.75"))

	// Prices are per month: the hostel's 4,000 a year counts as 500.
	assert.Equal(t, []uuid.UUID{cheap, hostel, mid}, search("sort=price_asc"))
	assert.Equal(t, []uuid.UUID{mid, hostel, cheap}, search("sort=price_desc"))
	assert.ElementsMatch(t, []uuid.UUID{cheap, hostel}, search("max=600"))
	assert.ElementsMatch(t, []uuid.UUID{hostel, mid}, search("min=450"))
	assert.ElementsMatch(t, []uuid.UUID{hostel}, search("min=600&max=450"), "swapped bounds still work")

	assert.ElementsMatch(t, []uuid.UUID{hostel}, search("type=hostels"))
	assert.ElementsMatch(t, []uuid.UUID{cheap, mid, hostel}, search("type=rooms&type=hostels"))
	assert.ElementsMatch(t, []uuid.UUID{mid, hostel}, search("sc=1"))
	assert.ElementsMatch(t, []uuid.UUID{mid, hostel}, search("feature=security"), "gated or cctv")
	assert.ElementsMatch(t, []uuid.UUID{mid}, search("feature=security&feature=parking"))
	assert.Empty(t, search("verified=1"), "the lister's ID isn't checked yet")
	c.User.UpdateOneID(a.UserID).SetIdentityVerifiedAt(time.Now()).ExecX(ctx)
	assert.ElementsMatch(t, []uuid.UUID{cheap, mid, hostel}, search("verified=1&owner=1&meter=1&kitchen=private"))
	assert.Empty(t, search("furnished=1"))
	// A tiny box around the exact pin finds nothing: search only knows the
	// approximate point, 150–400 m away (§6.1).
	assert.Empty(t, search("bbox=-1.5590,6.6695,-1.5586,6.6699"))
	near := search("sort=nearest&bbox=-1.5688,6.6597,-1.5488,6.6797")
	require.NotEmpty(t, near)
	assert.Equal(t, cheap, near[0])

	// Garbage is ignored rather than failing.
	assert.ElementsMatch(t, []uuid.UUID{cheap, mid, hostel}, search("bbox=nope&min=abc&type=castles&sort=random&page=-3"))
	assert.ElementsMatch(t, []uuid.UUID{cheap, mid, hostel}, search("bbox=-180,-90,180,90"), "a world-sized box falls back to the default")

	// Markers use the approximate point, with short price labels.
	ms, err := s.Markers(ctx, ParseFilter(url.Values{}))
	require.NoError(t, err)
	require.Len(t, ms, 3)
	labels := map[uuid.UUID]string{}
	for _, m := range ms {
		labels[m.ID] = m.Label
	}
	assert.Equal(t, "₵400", labels[cheap])
	assert.Equal(t, "₵1.2k", labels[mid])
	assert.Equal(t, "₵4k/yr", labels[hostel])
	for _, m := range ms {
		d, err := s.Load(ctx, a, m.ID)
		require.NoError(t, err)
		assert.Equal(t, *d.P.ApproxLat, m.Lat)
		assert.NotEqual(t, *d.P.Lat, m.Lat, "never the exact point")
	}

	// Similar: same family, nearest first, not itself.
	d, _ := s.Load(ctx, a, cheap)
	sim, err := s.Similar(ctx, d, 4)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{mid}, ids(sim), "rooms only; hostel is another family")

	n, err := s.Count(ctx, ParseFilter(url.Values{"max": {"600"}}))
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// Radius search around a place replaces the map box: Adum is outside the
	// default view, but inside 2 km of Kejetia.
	assert.ElementsMatch(t, []uuid.UUID{far}, search("near=kejetia&radius=2"))
	assert.ElementsMatch(t, []uuid.UUID{far}, search("q=kejetia&radius=2"), "the no-script form sends words")
	assert.ElementsMatch(t, []uuid.UUID{cheap, mid, hostel}, search("near=knust&radius=5"), "Adum is ~6 km out")
	assert.NotContains(t, search("near=knust&radius=2"), hostel, "Kotei is ~2.4 km from KNUST")
	// near without radius only moves the centre: the view still decides.
	assert.Len(t, search("near=kejetia"), 3)
	assert.Equal(t, cheap, search("near=ayeduase&sort=nearest")[0])
	assert.ElementsMatch(t, []uuid.UUID{cheap, mid, hostel}, search("near=atlantis&radius=2"), "unknown places are ignored")
}

func TestFilterRoundTrip(t *testing.T) {
	q := url.Values{"bbox": {"-1.60000,6.65000,-1.53000,6.70000"}, "min": {"300"}, "max": {"1500"}, "type": {"rooms", "hostels"},
		"beds": {"2"}, "sc": {"1"}, "feature": {"parking", "security"}, "verified": {"1"}, "from": {"2026-10-01"}, "sort": {"newest"}, "page": {"2"}}
	f := ParseFilter(q)
	assert.Equal(t, q.Encode(), f.Query().Encode())
	assert.Equal(t, 9, f.ActiveCount())
	assert.Empty(t, ParseFilter(url.Values{}).Query(), "defaults encode to nothing")

	f = ParseFilter(url.Values{"q": {"Tech"}})
	assert.Equal(t, "knust", f.Near, "typed words pick the best place")
	assert.Equal(t, 3, f.Radius, "with a 3 km radius")
	assert.Equal(t, "near=knust&radius=3", f.Query().Encode())
	assert.Equal(t, 0, ParseFilter(url.Values{"near": {"knust"}, "radius": {"4"}}).Radius, "only the offered radii")
	assert.Equal(t, "KNUST", ParseFilter(url.Values{}).Ref().Name)
	assert.Equal(t, "Kejetia Market", ParseFilter(url.Values{"near": {"kejetia"}}).Ref().Name)
}

func TestPriceLabel(t *testing.T) {
	for _, tc := range []struct {
		cedis  int64
		period string
		want   string
	}{
		{650, "month", "₵650"}, {1000, "month", "₵1k"}, {1250, "month", "₵1.3k"}, {4500, "academic_year", "₵4.5k/yr"},
		{2000, "semester", "₵2k/sem"}, {15000, "year", "₵15k/yr"},
	} {
		assert.Equal(t, tc.want, PriceLabel(money.Pesewas(tc.cedis*100), tc.period))
	}
}
