package messages

import (
	"context"
	"os"
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
	"rentmapgh/internal/platform/sms"
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
	_, err = d.SQL.ExecContext(ctx, "TRUNCATE notifications, reports, messages, conversations, viewings, saved_listings, agent_mandates, listing_media, listing_terms, listings, units, properties, audit_events, sessions, role_assignments, otp_codes, verification_files, verifications, agent_profiles, landlord_profiles, users")
	require.NoError(t, err)
	capture := &sms.Capture{}
	s := NewService(d.Ent, audit.New(d.Ent), capture, NewHub(), "https://rentmap.test")
	clock := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	lister := d.Ent.User.Create().SetPhone("+233200000001").SetName("Akua Owusu").SaveX(ctx)
	renter := d.Ent.User.Create().SetPhone("+233200000002").SetName("Kofi Mensah").SaveX(ctx)
	p := d.Ent.Property.Create().SetCreatedBy(lister.ID).SaveX(ctx)
	u := d.Ent.Unit.Create().SetPropertyID(p.ID).SaveX(ctx)
	l := d.Ent.Listing.Create().SetUnitID(u.ID).SetListerID(lister.ID).SetListerKind(listing.ListerKindOwner).
		SetStatus(listing.StatusActive).SetHeadline("Chamber and hall in Bomso").SaveX(ctx)
	return &fixture{s: s, c: d.Ent, sms: capture, clock: &clock, renter: Actor{UserID: renter.ID}, lister: Actor{UserID: lister.ID}, listing: l.ID}
}

func (f *fixture) tick(d time.Duration) { *f.clock = f.clock.Add(d) }

func TestConversation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	_, err := f.s.Start(ctx, f.lister, f.listing)
	assert.ErrorIs(t, err, ErrForbidden, "not with yourself")
	c, err := f.s.Start(ctx, f.renter, f.listing)
	require.NoError(t, err)
	again, err := f.s.Start(ctx, f.renter, f.listing)
	require.NoError(t, err)
	assert.Equal(t, c.ID, again.ID, "one conversation per renter and listing")

	// The lister's stream hears about new messages.
	events, cancel := f.s.Hub().Subscribe(f.lister.UserID)
	defer cancel()

	rt, err := f.s.Load(ctx, f.renter, c.ID)
	require.NoError(t, err)
	_, err = f.s.Send(ctx, f.renter, rt, "   ")
	assert.Error(t, err)
	m, err := f.s.Send(ctx, f.renter, rt, "Hi, is it still available? My number is 024 123 4567")
	require.NoError(t, err)
	assert.Equal(t, []string{"phone"}, m.Flags, "a phone number before a confirmed viewing")
	select {
	case e := <-events:
		assert.Equal(t, "message", e.Kind)
		assert.Equal(t, 1, e.Unread)
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	assert.Equal(t, "+233200000001", f.sms.Last().To, "the lister is texted the first time")
	assert.Contains(t, f.sms.Last().Body, "Kofi sent you a message about \"Chamber and hall in Bomso\"")

	n, _ := f.s.Unread(ctx, f.lister.UserID)
	assert.Equal(t, 1, n)
	n, _ = f.s.Unread(ctx, f.renter.UserID)
	assert.Zero(t, n, "your own message isn't unread")

	// The lister reads and replies with a scammy message.
	lt, err := f.s.Load(ctx, f.lister, c.ID)
	require.NoError(t, err)
	f.tick(time.Minute)
	mine, stop := f.s.Hub().Subscribe(f.lister.UserID)
	f.s.Read(ctx, lt)
	select {
	case e := <-mine:
		assert.Equal(t, "unread", e.Kind, "reading updates my badge; it isn't a read receipt for me")
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	stop()
	n, _ = f.s.Unread(ctx, f.lister.UserID)
	assert.Zero(t, n)
	rt, _ = f.s.Load(ctx, f.renter, c.ID)
	require.NotNil(t, rt.TheirReadAt(), "read receipt")
	assert.False(t, rt.TheirReadAt().Before(m.CreatedAt))

	reply, err := f.s.Send(ctx, f.lister, lt, "Yes! Send the commitment fee to my MoMo today only")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"momo", "booking_fee", "pressure"}, reply.Flags)
	sms := len(f.sms.Messages())
	assert.Equal(t, "+233200000002", f.sms.Last().To, "the renter's first SMS")
	f.tick(time.Minute)
	_, err = f.s.Send(ctx, f.lister, lt, "Hello?")
	require.NoError(t, err)
	assert.Len(t, f.sms.Messages(), sms, "at most one SMS per 6 hours")

	ms, err := f.s.Messages(ctx, rt, uuid.Nil)
	require.NoError(t, err)
	assert.Len(t, ms, 3)
	after, _ := f.s.Messages(ctx, rt, ms[0].ID)
	assert.Len(t, after, 2, "since the first")

	// A confirmed viewing unlocks phone numbers.
	assert.False(t, rt.Unlocked)
	f.c.Viewing.Create().SetListingID(f.listing).SetRenterID(f.renter.UserID).SetListerID(f.lister.UserID).
		SetStartsAt(f.clock.Add(48 * time.Hour)).SetStatus(viewing.StatusConfirmed).ExecX(ctx)
	rt, _ = f.s.Load(ctx, f.renter, c.ID)
	assert.True(t, rt.Unlocked)

	// Inbox.
	box, err := f.s.Inbox(ctx, f.renter.UserID)
	require.NoError(t, err)
	require.Len(t, box, 1)
	assert.True(t, box[0].Unread)
	assert.Equal(t, "Hello?", box[0].Last.Body)

	// Strangers can't open it.
	_, err = f.s.Load(ctx, Actor{UserID: uuid.New()}, c.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestReports(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c, _ := f.s.Start(ctx, f.renter, f.listing)
	lt, _ := f.s.Load(ctx, f.lister, c.ID)
	m, err := f.s.Send(ctx, f.lister, lt, "Pay first then I'll show you")
	require.NoError(t, err)
	rt, _ := f.s.Load(ctx, f.renter, c.ID)

	assert.Error(t, f.s.ReportMessage(ctx, f.lister, lt, m.ID, "scam", ""), "not your own")
	require.NoError(t, f.s.ReportMessage(ctx, f.renter, rt, m.ID, "scam", "Wants money before viewing"))
	require.NoError(t, f.s.ReportMessage(ctx, f.renter, rt, m.ID, "scam", ""), "a second report is a no-op")
	open, err := f.s.OpenReports(ctx)
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, f.lister.UserID, *open[0].SubjectID)

	rc, err := f.s.ForReview(ctx, open[0].ID)
	require.NoError(t, err)
	assert.Equal(t, m.ID, rc.Message.ID)
	assert.Len(t, rc.Thread, 1)
	require.NoError(t, f.s.Resolve(ctx, Actor{UserID: uuid.New()}, open[0].ID, true))
	open, _ = f.s.OpenReports(ctx)
	assert.Empty(t, open)
}
