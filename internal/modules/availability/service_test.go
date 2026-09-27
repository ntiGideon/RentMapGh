package availability

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/viewing"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/viewings"
	"rentmapgh/internal/platform/sms"
)

type fixture struct {
	s      *Service
	c      *ent.Client
	sms    *sms.Capture
	clock  *time.Time
	lister Actor
	renter Actor
}

func setup(t *testing.T) *fixture {
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
	_, err = d.SQL.ExecContext(ctx, "TRUNCATE notifications, reports, messages, conversations, viewings, saved_listings, agent_mandates, listing_media, listing_terms, listing_stats, listings, units, properties, audit_events, sessions, role_assignments, otp_codes, verification_files, verifications, agent_profiles, landlord_profiles, users")
	require.NoError(t, err)
	capture := &sms.Capture{}
	clock := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC) // daytime
	vs := viewings.NewService(d.Ent, audit.New(d.Ent), capture, "https://rentmap.test")
	s := NewService(d.Ent, audit.New(d.Ent), capture, vs, "test-secret-test-secret-test-secret", "https://rentmap.test")
	s.now = func() time.Time { return clock }
	lister := d.Ent.User.Create().SetPhone("+233200000001").SetName("Akua").SaveX(ctx)
	renter := d.Ent.User.Create().SetPhone("+233200000002").SetName("Kofi").SaveX(ctx)
	return &fixture{s: s, c: d.Ent, sms: capture, clock: &clock, lister: Actor{UserID: lister.ID}, renter: Actor{UserID: renter.ID}}
}

// live makes an active listing last confirmed `ago`.
func (f *fixture) live(t *testing.T, ago time.Duration) *ent.Listing {
	t.Helper()
	ctx := context.Background()
	p := f.c.Property.Create().SetCreatedBy(f.lister.UserID).SaveX(ctx)
	u := f.c.Unit.Create().SetPropertyID(p.ID).SaveX(ctx)
	confirmed := f.clock.Add(-ago)
	return f.c.Listing.Create().SetUnitID(u.ID).SetListerID(f.lister.UserID).SetListerKind(listing.ListerKindOwner).
		SetStatus(listing.StatusActive).SetHeadline("Chamber and hall in Bomso").
		SetPublishedAt(confirmed).SetLastConfirmedAt(confirmed).SaveX(ctx)
}

func (f *fixture) at(d time.Duration) { *f.clock = f.clock.Add(d) }

func TestToken(t *testing.T) {
	f := setup(t)
	id := uuid.Must(uuid.NewV7())
	tok := f.s.Token(id)
	assert.Less(t, len(f.s.Link(id)), 70, "short enough for an SMS")
	got, err := f.s.Open(tok)
	require.NoError(t, err)
	assert.Equal(t, id, got)

	_, err = f.s.Open(tok[:len(tok)-2] + "xx")
	assert.ErrorIs(t, err, ErrBadLink, "tampered")
	other := f.s.Token(uuid.Must(uuid.NewV7()))
	_, err = f.s.Open(strings.Split(other, ".")[0] + "." + strings.Split(tok, ".")[1])
	assert.ErrorIs(t, err, ErrBadLink, "signature from another listing")
	f.at(LinkTTL + time.Minute)
	_, err = f.s.Open(tok)
	assert.ErrorIs(t, err, ErrBadLink, "expired")
}

func TestSweep(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	fresh := f.live(t, 24*time.Hour)
	ageing := f.live(t, 4*24*time.Hour)
	old := f.live(t, 15*24*time.Hour)

	expired, nudged, err := f.s.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, expired)
	assert.Equal(t, 1, nudged)
	assert.Equal(t, listing.StatusExpired, f.c.Listing.GetX(ctx, old.ID).Status)
	assert.Equal(t, listing.StatusActive, f.c.Listing.GetX(ctx, fresh.ID).Status)
	msgs := f.sms.Messages()
	require.Len(t, msgs, 2)
	assert.Contains(t, msgs[0].Body, "hidden from search")
	assert.Contains(t, msgs[1].Body, "is \"Chamber and hall in Bomso\" still available? Tap to answer (no login): https://rentmap.test/c/")
	assert.LessOrEqual(t, len(msgs[1].Body), 160)

	// Not again the same week; again after 7 days; never at night.
	_, nudged, _ = f.s.Sweep(ctx)
	assert.Zero(t, nudged)
	f.at(RenudgeAfter)
	*f.clock = time.Date(f.clock.Year(), f.clock.Month(), f.clock.Day(), 22, 0, 0, 0, time.UTC)
	_, nudged, _ = f.s.Sweep(ctx)
	assert.Zero(t, nudged, "quiet hours")
	f.at(12 * time.Hour) // 10:00 next day
	_, nudged, _ = f.s.Sweep(ctx)
	assert.Equal(t, 2, nudged, "the ageing one again, and the once-fresh one for the first time")

	// Freshness and archiving.
	now := f.s.now()
	assert.True(t, Fresh(f.c.Listing.GetX(ctx, fresh.ID), fresh.LastConfirmedAt.Add(time.Hour)))
	assert.False(t, Fresh(f.c.Listing.GetX(ctx, ageing.ID), now))
	o := f.c.Listing.GetX(ctx, old.ID)
	assert.False(t, Archived(o, now))
	assert.True(t, Archived(o, o.LastConfirmedAt.Add(ExpireAfter+ArchiveAfter+time.Hour)))
}

func TestConfirmAnswers(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	l := f.live(t, 15*24*time.Hour)
	_, _, _ = f.s.Sweep(ctx) // expires it

	_, err := f.s.Confirm(ctx, f.renter, l.ID, StillAvailable, "")
	assert.ErrorIs(t, err, ErrNotFound, "only the lister")
	got, err := f.s.Confirm(ctx, f.lister, l.ID, StillAvailable, "")
	require.NoError(t, err)
	assert.Equal(t, listing.StatusActive, got.Status, "back in search")
	assert.WithinDuration(t, f.s.now(), *got.LastConfirmedAt, time.Second)
	assert.True(t, Fresh(got, f.s.now()))

	got, err = f.s.Confirm(ctx, f.lister, l.ID, PauseIt, "")
	require.NoError(t, err)
	assert.Equal(t, listing.StatusPaused, got.Status)

	// Rented: open viewings are declined and the renters told.
	f.c.Viewing.Create().SetListingID(l.ID).SetRenterID(f.renter.UserID).SetListerID(f.lister.UserID).
		SetStartsAt(f.s.now().Add(48 * time.Hour)).SetStatus(viewing.StatusConfirmed).ExecX(ctx)
	got, err = f.s.Confirm(ctx, f.lister, l.ID, IsRented, "rentmap")
	require.NoError(t, err)
	assert.Equal(t, listing.StatusRented, got.Status)
	assert.Equal(t, listing.RentedViaRentmap, *got.RentedVia)
	assert.NotNil(t, got.RentedAt)
	assert.Equal(t, viewing.StatusDeclined, f.c.Viewing.Query().OnlyX(ctx).Status)
	assert.Equal(t, "+233200000002", f.sms.Last().To)
	assert.Contains(t, f.sms.Last().Body, "it's already rented")

	// Answering again only updates the question.
	got, err = f.s.Confirm(ctx, f.lister, l.ID, IsRented, "elsewhere")
	require.NoError(t, err)
	assert.Equal(t, listing.RentedViaElsewhere, *got.RentedVia)
	_, err = f.s.Confirm(ctx, f.lister, l.ID, PauseIt, "")
	assert.Error(t, err)
}

func TestReportRented(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	l := f.live(t, time.Hour)
	assert.True(t, Fresh(f.c.Listing.GetX(ctx, l.ID), f.s.now()))

	require.NoError(t, f.s.ReportRented(ctx, f.renter, l.ID))
	got := f.c.Listing.GetX(ctx, l.ID)
	assert.False(t, Fresh(got, f.s.now()), "no longer shown as confirmed")
	assert.Equal(t, "+233200000001", f.sms.Last().To, "the lister is asked at once (daytime)")
	assert.Contains(t, f.sms.Last().Body, "a renter says")
	n := len(f.sms.Messages())
	require.NoError(t, f.s.ReportRented(ctx, f.renter, l.ID), "a repeat is a no-op")
	assert.Len(t, f.sms.Messages(), n)
	assert.ErrorIs(t, f.s.ReportRented(ctx, f.lister, l.ID), ErrNotFound, "not your own")

	f.at(time.Hour)
	got, err := f.s.Confirm(ctx, f.lister, l.ID, StillAvailable, "")
	require.NoError(t, err)
	assert.True(t, Fresh(got, f.s.now()), "confirming clears the report")
	assert.Nil(t, got.StaleReportedAt)
}
