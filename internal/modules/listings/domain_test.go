package listings

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/platform/money"
)

func ptr[T any](v T) *T { return &v }

func cedis(n int64) *money.Pesewas { p := money.Pesewas(n * 100); return &p }

func TestMoveInFromTheLandingPageExample(t *testing.T) {
	// Chamber & hall at GH₵ 1,200/month, 6 months advance, agent fee 600,
	// service charge 250 → GH₵ 8,050 (the figure on the landing page).
	m := ComputeMoveIn(Terms{
		Rent: cedis(1200), Period: "month", AdvancePeriods: ptr(6),
		Deposit: cedis(0), AgentFee: cedis(600), ServiceCharge: cedis(250), ViewingFee: cedis(0),
	})
	require.True(t, m.Complete, m.Missing)
	assert.Equal(t, money.Pesewas(805000), m.Total)
	assert.Equal(t, "6 months", m.UpfrontLabel)
	assert.Equal(t, []Line{
		{"Rent × 6 months upfront", 720000},
		{"Agent fee", 60000},
		{"Service charge", 25000},
	}, m.Lines, "zero-value fees are hidden from the breakdown")
}

func TestMoveInFlagsUnansweredFees(t *testing.T) {
	m := ComputeMoveIn(Terms{Rent: cedis(3200), Period: "academic_year", AdvancePeriods: ptr(1)})
	assert.False(t, m.Complete)
	assert.Equal(t, []string{"Deposit", "Agent fee", "Service charge", "Viewing fee"}, m.Missing)
	assert.Equal(t, money.Pesewas(320000), m.Total, "partial total still shown in the live preview")
	assert.Equal(t, money.Pesewas(40000), m.Monthly, "3,200 over an 8-month academic year")
}

func TestMoveInOtherFeesAndViewingFee(t *testing.T) {
	m := ComputeMoveIn(Terms{
		Rent: cedis(9000), Period: "year", AdvancePeriods: ptr(2),
		Deposit: cedis(500), AgentFee: cedis(0), ServiceCharge: cedis(0), ViewingFee: cedis(50),
		OtherFees: []Fee{{Label: "Sanitation", Amount: 12000}, {Label: "Key", Amount: 0}},
	})
	assert.True(t, m.Complete)
	assert.Equal(t, money.Pesewas(9000*2*100+500*100+12000), m.Total, "viewing fee is not part of move-in")
	assert.Equal(t, "2 years", m.UpfrontLabel)
	assert.Equal(t, money.Pesewas(75000), m.Monthly)
}

func TestMonthlyEquivalentRounding(t *testing.T) {
	p, _ := rentPeriod("semester")
	assert.Equal(t, money.Pesewas(33300), MonthlyEquivalent(133300, p), "1,333 / 4 = 333.25 → 333")
	m, _ := rentPeriod("month")
	assert.Equal(t, money.Pesewas(85050), MonthlyEquivalent(85050, m), "monthly rent is exact")
}

func TestStateMachine(t *testing.T) {
	type step struct {
		ev   Event
		want Status
	}
	paths := map[string][]step{
		"verified lister goes live": {{EvSubmitVerified, Active}, {EvPause, Paused}, {EvResume, Active}, {EvMarkRented, Rented}, {EvRelist, Active}},
		"review then approve":       {{EvSubmitUnverified, PendingReview}, {EvApprove, Active}},
		"review sent back":          {{EvSubmitUnverified, PendingReview}, {EvRequestChanges, Draft}, {EvSubmitVerified, Active}},
		"expiry and re-confirm":     {{EvSubmitVerified, Active}, {EvExpire, Expired}, {EvResume, Active}},
		"takedown is final":         {{EvSubmitVerified, Active}, {EvRemove, Removed}},
	}
	for name, steps := range paths {
		t.Run(name, func(t *testing.T) {
			s := Draft
			for _, st := range steps {
				next, err := Next(s, st.ev)
				require.NoError(t, err, "%s from %s", st.ev, s)
				assert.Equal(t, st.want, next)
				s = next
			}
		})
	}

	refused := []struct {
		from Status
		ev   Event
	}{
		{Draft, EvApprove}, // can't approve what wasn't submitted
		{Draft, EvPause},   // not live yet
		{PendingReview, EvSubmitVerified},
		{Active, EvApprove},
		{Rented, EvPause},
		{Removed, EvResume}, // takedowns stick
		{Removed, EvRelist},
		{Expired, EvMarkRented},
	}
	for _, r := range refused {
		_, err := Next(r.from, r.ev)
		assert.ErrorIs(t, err, ErrTransition, "%s from %s", r.ev, r.from)
	}

	// Every status appears in the table (no silent dead ends).
	for _, s := range []Status{Draft, PendingReview, Active, Paused, Rented, Expired, Removed} {
		_, ok := transitions[s]
		assert.True(t, ok, s)
	}
}

func TestQuality(t *testing.T) {
	full := QualityInput{HasLocation: true, HasLandmark: true, HasDigitalAddress: true, HeadlineLen: 40,
		DescriptionLen: 400, Amenities: 5, FeesComplete: true, HasAvailableFrom: true, Photos: 10, HasVideo: true}
	score, tips := Quality(full)
	assert.Equal(t, 100, score)
	assert.Empty(t, tips)

	score, tips = Quality(QualityInput{HasLocation: true, FeesComplete: true, Photos: 5})
	assert.Equal(t, 30, score)
	require.NotEmpty(t, tips)
	assert.Equal(t, "Add 3 more photos to get 2× more views", tips[0].Text, "biggest win first")
	assert.Equal(t, "photos", tips[0].Step)
}
