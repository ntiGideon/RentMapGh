package viewings

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
	"rentmapgh/internal/platform/money"
	"rentmapgh/internal/platform/sms"
	"rentmapgh/internal/platform/weekly"
)

type fixture struct {
	s       *Service
	c       *ent.Client
	sms     *sms.Capture
	clock   *time.Time
	renter  Actor
	lister  Actor
	listing uuid.UUID
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
	_, err = d.SQL.ExecContext(ctx, "TRUNCATE viewings, saved_listings, agent_mandates, listing_media, listing_terms, listings, units, properties, audit_events, sessions, role_assignments, otp_codes, verification_files, verifications, agent_profiles, landlord_profiles, users")
	require.NoError(t, err)

	capture := &sms.Capture{}
	s := NewService(d.Ent, audit.New(d.Ent), capture, "https://rentmap.test")
	clock := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC) // a Thursday
	s.now = func() time.Time { return clock }

	lister := d.Ent.User.Create().SetPhone("+233200000001").SetName("Akua Owusu").
		SetViewingHours([]weekly.Window{{Day: 6, Start: 9 * 60, End: 12 * 60}}).SaveX(ctx) // Saturdays 9–12
	renter := d.Ent.User.Create().SetPhone("+233200000002").SetName("Kofi Mensah").SaveX(ctx)
	p := d.Ent.Property.Create().SetCreatedBy(lister.ID).SetLat(6.6697).SetLng(-1.5588).SetStreet("Lagos Avenue").
		SetLandmark("Behind the police station").SetDigitalAddress("AK-039-5028").SaveX(ctx)
	u := d.Ent.Unit.Create().SetPropertyID(p.ID).SaveX(ctx)
	l := d.Ent.Listing.Create().SetUnitID(u.ID).SetListerID(lister.ID).SetListerKind(listing.ListerKindOwner).
		SetStatus(listing.StatusActive).SetHeadline("Chamber and hall in Bomso").SaveX(ctx)
	d.Ent.ListingTerms.Create().SetListingID(l.ID).SetViewingFee(money.Pesewas(2000)).SaveX(ctx)
	return &fixture{s: s, c: d.Ent, sms: capture, clock: &clock,
		renter: Actor{UserID: renter.ID}, lister: Actor{UserID: lister.ID}, listing: l.ID}
}

var sat10 = time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)

func TestRequestAcceptAndUnlock(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	p, err := f.s.Bookable(ctx, f.renter, f.listing)
	require.NoError(t, err)
	slots, err := f.s.Slots(ctx, p)
	require.NoError(t, err)
	require.NotEmpty(t, slots)
	assert.Equal(t, time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC), slots[0].UTC(), "first Saturday slot")
	assert.Len(t, slots, 12, "two Saturdays × six half hours")

	// The viewing fee must be acknowledged.
	_, err = f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: sat10})
	assert.Contains(t, err.(ValidationError)["fee_ack"], "viewing fee")
	// Off-slot times are refused when the lister has hours.
	_, err = f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: sat10.Add(15 * time.Minute), FeeAck: true})
	assert.Contains(t, err.(ValidationError)["starts_at"], "just taken")

	v, err := f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: sat10, FeeAck: true, Note: "Can I come with my sister?"})
	require.NoError(t, err)
	assert.Equal(t, viewing.StatusRequested, v.Status)
	assert.Equal(t, money.Pesewas(2000), *v.ViewingFee)
	msg := f.sms.Last()
	assert.Equal(t, "+233200000001", msg.To)
	assert.Contains(t, msg.Body, "Kofi wants to view \"Chamber and hall in Bomso\" on Sat 3 Oct, 10:00")
	assert.Contains(t, msg.Body, "https://rentmap.test/viewings/"+v.ID.String())

	_, err = f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: sat10.Add(time.Hour), FeeAck: true})
	assert.Contains(t, err.(ValidationError)["form"], "already have a viewing")
	_, err = f.s.Bookable(ctx, f.lister, f.listing)
	assert.ErrorIs(t, err, ErrForbidden, "listers can't book their own place")

	// Not unlocked while only requested.
	d, err := f.s.Load(ctx, f.renter, v.ID)
	require.NoError(t, err)
	assert.False(t, f.s.Unlocked(d.V))
	_, err = f.s.Load(ctx, Actor{UserID: uuid.New()}, v.ID)
	assert.ErrorIs(t, err, ErrNotFound, "strangers can't see it")

	// The renter can't accept their own request.
	_, err = f.s.Act(ctx, f.renter, v.ID, Accept, Answer{})
	assert.Error(t, err)

	d, err = f.s.Act(ctx, f.lister, v.ID, Accept, Answer{})
	require.NoError(t, err)
	assert.Equal(t, viewing.StatusConfirmed, d.V.Status)
	assert.True(t, f.s.Unlocked(d.V))
	assert.Equal(t, "+233200000002", f.sms.Last().To)
	assert.Contains(t, f.sms.Last().Body, "is confirmed")

	// The confirmed slot is no longer offered.
	slots, _ = f.s.Slots(ctx, p)
	for _, s := range slots {
		assert.False(t, s.Equal(sat10))
	}

	// Opening the location is logged once.
	d, _ = f.s.Load(ctx, f.renter, v.ID)
	f.s.SeeLocation(ctx, f.renter, d)
	f.s.SeeLocation(ctx, f.renter, d)
	assert.NotNil(t, d.V.LocationSeenAt)
	n := f.c.AuditEvent.Query().CountX(ctx)
	assert.Equal(t, 3, n, "requested, accepted, location unlocked")

	// A day after the time, the location locks again; the lister records the outcome.
	*f.clock = sat10.Add(25 * time.Hour)
	assert.False(t, f.s.Unlocked(d.V))
	d, err = f.s.Act(ctx, f.lister, v.ID, Done, Answer{})
	require.NoError(t, err)
	assert.Equal(t, viewing.StatusCompleted, d.V.Status)
}

func TestProposeDeclineCancel(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	v, err := f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: sat10, FeeAck: true})
	require.NoError(t, err)

	// The lister proposes Sunday 15:00 (outside their own hours is fine for a proposal).
	sun := time.Date(2026, 10, 4, 15, 0, 0, 0, time.UTC)
	_, err = f.s.Act(ctx, f.lister, v.ID, Propose, Answer{StartsAt: sun.Add(10 * time.Minute)})
	assert.Error(t, err, "on the hour or half hour")
	d, err := f.s.Act(ctx, f.lister, v.ID, Propose, Answer{StartsAt: sun})
	require.NoError(t, err)
	assert.Equal(t, viewing.StatusProposed, d.V.Status)
	assert.True(t, d.V.StartsAt.Equal(sun))
	assert.Contains(t, f.sms.Last().Body, "suggests Sun 4 Oct, 15:00 instead")

	d, err = f.s.Act(ctx, f.renter, v.ID, AcceptProposal, Answer{})
	require.NoError(t, err)
	assert.Equal(t, viewing.StatusConfirmed, d.V.Status)
	assert.Equal(t, "+233200000001", f.sms.Last().To)

	// Cancelling a confirmed viewing tells the other side.
	d, err = f.s.Act(ctx, f.renter, v.ID, Cancel, Answer{})
	require.NoError(t, err)
	assert.Equal(t, viewing.StatusCancelled, d.V.Status)
	assert.Contains(t, f.sms.Last().Body, "was cancelled")
	assert.Equal(t, f.renter.UserID, *d.V.ClosedBy)

	// A new request can be made, and declined with a reason.
	v2, err := f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: sat10, FeeAck: true})
	require.NoError(t, err)
	d, err = f.s.Act(ctx, f.lister, v2.ID, Decline, Answer{Reason: "rented"})
	require.NoError(t, err)
	assert.Equal(t, viewing.StatusDeclined, d.V.Status)
	assert.Contains(t, f.sms.Last().Body, "it's already rented")
	_, err = f.s.Act(ctx, f.lister, v2.ID, Accept, Answer{})
	assert.Error(t, err, "can't accept a declined viewing")
}

func TestClashAndLimits(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := Actor{UserID: f.c.User.Create().SetPhone("+233200000003").SetName("Yaw").SaveX(ctx).ID}

	// Two renters can request the same slot; only one can be confirmed.
	v1, err := f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: sat10, FeeAck: true})
	require.NoError(t, err)
	v2, err := f.s.Ask(ctx, other, f.listing, Request{StartsAt: sat10, FeeAck: true})
	require.NoError(t, err)
	_, err = f.s.Act(ctx, f.lister, v1.ID, Accept, Answer{})
	require.NoError(t, err)
	_, err = f.s.Act(ctx, f.lister, v2.ID, Accept, Answer{})
	assert.Contains(t, err.(ValidationError)["form"], "already confirmed")

	// Without viewing hours, renters suggest a daytime time.
	f.c.User.UpdateOneID(f.lister.UserID).ClearViewingHours().ExecX(ctx)
	f.c.Viewing.Delete().ExecX(ctx)
	_, err = f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC), FeeAck: true})
	assert.Contains(t, err.(ValidationError)["starts_at"], "between 7:00 and 18:00")
	_, err = f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), FeeAck: true})
	assert.Contains(t, err.(ValidationError)["starts_at"], "3 hours")
	_, err = f.s.Ask(ctx, f.renter, f.listing, Request{StartsAt: time.Date(2026, 10, 2, 16, 30, 0, 0, time.UTC), FeeAck: true})
	require.NoError(t, err)

	// Only live listings can be booked.
	f.c.Listing.UpdateOneID(f.listing).SetStatus(listing.StatusPaused).ExecX(ctx)
	_, err = f.s.Bookable(ctx, other, f.listing)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestICS(t *testing.T) {
	d := &Detail{V: &ent.Viewing{ID: uuid.MustParse("01a0e3f0-0000-7000-8000-000000000001"), StartsAt: sat10, DurationMin: 30},
		Place: &Place{L: &ent.Listing{Headline: "Chamber and hall, Bomso; near KNUST"}}}
	ics := ICS(d, "Lagos Avenue, Bomso", "https://rentmap.test/viewings/x", sat10.Add(-48*time.Hour))
	assert.Contains(t, ics, "DTSTART:20261003T100000Z\r\n")
	assert.Contains(t, ics, "DTEND:20261003T103000Z\r\n")
	assert.Contains(t, ics, `SUMMARY:Viewing: Chamber and hall\, Bomso\; near KNUST`)
	assert.Contains(t, ics, "TRIGGER:-PT2H")
	for _, l := range strings.Split(ics, "\r\n") {
		assert.LessOrEqual(t, len(l), 75, "folded: %q", l)
	}
}
