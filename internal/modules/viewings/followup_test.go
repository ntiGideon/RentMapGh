package viewings

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/report"
	"rentmapgh/internal/ent/viewing"
)

// confirmed makes a confirmed viewing starting at `at`.
func (f *fixture) confirmed(t *testing.T, renter uuid.UUID, at time.Time) *ent.Viewing {
	t.Helper()
	return f.c.Viewing.Create().SetListingID(f.listing).SetRenterID(renter).SetListerID(f.lister.UserID).
		SetStartsAt(at).SetStatus(viewing.StatusConfirmed).SaveX(context.Background())
}

func TestRemindersAndFeedbackPrompt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	v := f.confirmed(t, f.renter.UserID, f.clock.Add(20*time.Hour))

	n, err := f.s.Remind(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "24 h reminder to both")
	assert.Contains(t, f.sms.Last().Body, "tomorrow at")
	n, _ = f.s.Remind(ctx)
	assert.Zero(t, n, "once")

	*f.clock = v.StartsAt.Add(-90 * time.Minute)
	n, _ = f.s.Remind(ctx)
	assert.Equal(t, 2, n, "2 h reminder")
	assert.Contains(t, f.sms.Last().Body, "in 2 hours")

	*f.clock = v.StartsAt.Add(time.Hour)
	n, _ = f.s.Remind(ctx)
	assert.Zero(t, n, "not yet: feedback waits 2 h")
	*f.clock = v.StartsAt.Add(2*time.Hour + time.Minute)
	n, _ = f.s.Remind(ctx)
	assert.Equal(t, 2, n, "feedback prompt (renter) + outcome prompt (lister)")
	n, _ = f.s.Remind(ctx)
	assert.Zero(t, n)
}

func TestFeedback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	v := f.confirmed(t, f.renter.UserID, f.clock.Add(24*time.Hour))

	_, err := f.s.GiveFeedback(ctx, f.renter, v.ID, Feedback{Outcome: "happened", Accuracy: "as_described"})
	assert.Contains(t, err.(ValidationError)["form"], "after the viewing")
	*f.clock = v.StartsAt.Add(3 * time.Hour)
	_, err = f.s.GiveFeedback(ctx, f.lister, v.ID, Feedback{Outcome: "happened"})
	assert.ErrorIs(t, err, ErrNotFound, "renters only")
	_, err = f.s.GiveFeedback(ctx, f.renter, v.ID, Feedback{Outcome: "happened"})
	assert.Contains(t, err.(ValidationError)["accuracy"], "as described")

	yes := true
	d, err := f.s.GiveFeedback(ctx, f.renter, v.ID, Feedback{Outcome: "happened", Accuracy: "as_described", Interested: &yes, Note: "Lovely"})
	require.NoError(t, err)
	assert.Equal(t, viewing.StatusCompleted, d.V.Status)
	assert.Equal(t, viewing.AccuracyAsDescribed, *d.V.Accuracy)
	assert.True(t, *d.V.Interested)
	_, err = f.s.GiveFeedback(ctx, f.renter, v.ID, Feedback{Outcome: "happened", Accuracy: "mostly"})
	assert.Contains(t, err.(ValidationError)["form"], "already told us")
}

func TestNotAsDescribedOpensACase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i := range NotAsDescribedAt {
		r := f.c.User.Create().SetPhone("+23320000010" + string(rune('0'+i))).SetName("R").SaveX(ctx)
		v := f.confirmed(t, r.ID, f.clock.Add(-4*time.Hour))
		_, err := f.s.GiveFeedback(ctx, Actor{UserID: r.ID}, v.ID, Feedback{Outcome: "happened", Accuracy: "not_as_described"})
		require.NoError(t, err)
	}
	rs := f.c.Report.Query().Where(report.TargetTypeEQ(report.TargetTypeListing), report.ReasonEQ("not_as_described")).AllX(ctx)
	require.Len(t, rs, 1, "one case, opened at the second report")
	assert.Equal(t, f.lister.UserID, *rs[0].SubjectID)
}

func TestReliability(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	past := f.clock.Add(-48 * time.Hour)
	for i := range 3 {
		f.c.Viewing.Create().SetListingID(f.listing).SetRenterID(f.renter.UserID).SetListerID(f.lister.UserID).
			SetStartsAt(past.Add(time.Duration(i) * time.Hour)).SetStatus(viewing.StatusCompleted).
			SetAccuracy(viewing.AccuracyAsDescribed).SetCreatedAt(past.Add(-72 * time.Hour)).
			SetRespondedAt(past.Add(-70 * time.Hour)).ExecX(ctx)
	}
	r, err := f.s.ReliabilityOf(ctx, f.lister.UserID)
	require.NoError(t, err)
	assert.True(t, r.RepliesFast(), "3 requests answered within 2 h")
	assert.True(t, r.ShowsUp())
	assert.True(t, r.Accurate())

	// A renter report that the lister didn't come takes "reliable" away.
	v := f.confirmed(t, f.renter.UserID, past)
	f.c.Viewing.UpdateOne(v).SetRenterOutcome(viewing.RenterOutcomeListerMissed).ExecX(ctx)
	r, _ = f.s.ReliabilityOf(ctx, f.lister.UserID)
	assert.False(t, r.ShowsUp())

	rr, _ := f.s.ReliabilityOf(ctx, f.renter.UserID)
	assert.Equal(t, 3, rr.Attended, "as a renter")
}
