package mandates

import (
	"context"
	"errors"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/agentmandate"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/sms"
)

type fixture struct {
	s       *Service
	c       *ent.Client
	sms     *sms.Capture
	clock   *time.Time
	agent   Actor
	listing uuid.UUID
	prop    uuid.UUID
}

var linkRe = regexp.MustCompile(`https://rentmap\.test/m/([A-Za-z0-9_-]{43})`)

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
	s := NewService(d.Ent, audit.New(d.Ent), capture, "test-secret-test-secret-test-secret", "https://rentmap.test/")
	clock := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	agent := d.Ent.User.Create().SetPhone("+233244000001").SetName("Kwame Mensah").SaveX(ctx)
	p := d.Ent.Property.Create().SetCreatedBy(agent.ID).SetName("Adom Hostel").SaveX(ctx)
	u := d.Ent.Unit.Create().SetPropertyID(p.ID).SaveX(ctx)
	l := d.Ent.Listing.Create().SetUnitID(u.ID).SetListerID(agent.ID).SetListerKind(listing.ListerKindAgent).SaveX(ctx)
	return &fixture{s: s, c: d.Ent, sms: capture, clock: &clock, agent: Actor{UserID: agent.ID, IP: "192.0.2.1"}, listing: l.ID, prop: p.ID}
}

func (f *fixture) advance(d time.Duration) {
	*f.clock = f.clock.Add(d)
}

func (f *fixture) ask(t *testing.T) string {
	t.Helper()
	_, err := f.s.Ask(context.Background(), f.agent, f.listing, Request{LandlordName: "Mrs  Owusu", LandlordPhone: "020 111 2222", Months: 6})
	require.NoError(t, err)
	m := linkRe.FindStringSubmatch(f.sms.Last().Body)
	require.Len(t, m, 2, f.sms.Last().Body)
	return m[1]
}

func TestAskAndApprove(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	token := f.ask(t)
	msg := f.sms.Last()
	assert.Equal(t, "+233201112222", msg.To)
	assert.Contains(t, msg.Body, "Kwame Mensah (024 400 0001) asks to list \"Adom Hostel\"")
	assert.LessOrEqual(t, len(msg.Body), 160, "one SMS segment")

	m, err := f.s.Latest(ctx, f.agent.UserID, f.prop)
	require.NoError(t, err)
	assert.Equal(t, "Mrs Owusu", m.LandlordName)
	assert.Equal(t, Pending, StateOf(m, f.s.Now()))
	assert.NotContains(t, string(m.TokenHash), token, "only the hash is stored")

	// The landlord's page.
	o, err := f.s.ByToken(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, Pending, o.State)
	assert.Equal(t, "Kwame Mensah", o.Agent.Name)
	assert.Equal(t, "Adom Hostel", o.P.Name)
	_, err = f.s.ByToken(ctx, token[:42]+"x")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = f.s.ByToken(ctx, "short")
	assert.ErrorIs(t, err, ErrNotFound)

	// A landlord with an account becomes the property's owner.
	landlord := f.c.User.Create().SetPhone("+233201112222").SetName("Akua Owusu").SaveX(ctx)
	o, err = f.s.Decide(ctx, token, Approve, Visitor{IP: "198.51.100.7"})
	require.NoError(t, err)
	assert.Equal(t, Approved, o.State)
	assert.WithinDuration(t, f.clock.AddDate(0, 6, 0), *o.M.ValidUntil, 0)
	assert.Equal(t, landlord.ID, *o.M.GrantedBy)
	assert.Equal(t, landlord.ID, *f.c.Property.GetX(ctx, f.prop).OwnerID)
	assert.Equal(t, "+233244000001", f.sms.Last().To, "the agent hears back")
	assert.Contains(t, f.sms.Last().Body, "approved you to list Adom Hostel")
	assert.Equal(t, "Owner authority confirmed", Label(o.State))

	// A second tap does nothing; asking again is refused.
	_, err = f.s.Decide(ctx, token, Approve, Visitor{})
	assert.Error(t, err)
	_, err = f.s.Ask(ctx, f.agent, f.listing, Request{LandlordPhone: "0201112222"})
	assert.Equal(t, ValidationError{"form": "The owner has already approved this property."}, err)

	states, err := f.s.States(ctx, f.agent.UserID, []uuid.UUID{f.prop, uuid.New()})
	require.NoError(t, err)
	assert.Equal(t, map[uuid.UUID]State{f.prop: Approved}, states)

	// Withdraw through the same link.
	o, err = f.s.Decide(ctx, token, Withdraw, Visitor{})
	require.NoError(t, err)
	assert.Equal(t, Revoked, o.State)
	assert.Contains(t, f.sms.Last().Body, "withdrew")
	assert.Equal(t, "Agent listing — owner authority not confirmed", Label(o.State))

	n := f.c.AuditEvent.Query().CountX(ctx)
	assert.Equal(t, 3, n, "requested, approved, revoked")
}

func TestApprovalLapses(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	token := f.ask(t)
	_, err := f.s.Decide(ctx, token, Approve, Visitor{})
	require.NoError(t, err)
	f.advance(183 * 24 * time.Hour)
	m, _ := f.s.Latest(ctx, f.agent.UserID, f.prop)
	assert.Equal(t, Expired, StateOf(m, f.s.Now()))
	_, err = f.s.Decide(ctx, token, Withdraw, Visitor{})
	assert.Error(t, err, "nothing left to withdraw")
	f.ask(t) // a lapsed approval can be renewed
}

func TestDeclineReportAndCooloff(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	token := f.ask(t)
	o, err := f.s.Decide(ctx, token, Report, Visitor{})
	require.NoError(t, err)
	assert.Equal(t, Declined, o.State)
	assert.True(t, o.M.Reported)
	assert.Contains(t, f.sms.Last().Body, "declined")

	_, err = f.s.Ask(ctx, f.agent, f.listing, Request{LandlordPhone: "0201112222"})
	assert.Equal(t, ValidationError{"form": "The owner said no recently. Speak with them before asking again."}, err)
	f.advance(DeclineCooloff)
	f.ask(t)
}

func TestResendCancelAndLimits(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	first := f.ask(t)

	_, err := f.s.Ask(ctx, f.agent, f.listing, Request{LandlordPhone: "0201112222"})
	assert.Contains(t, err.(ValidationError)["form"], "already waiting")

	assert.Contains(t, f.s.Resend(ctx, f.agent, f.listing).(ValidationError)["form"], "Wait a few minutes")
	f.advance(ResendCooldown)
	require.NoError(t, f.s.Resend(ctx, f.agent, f.listing))
	second := linkRe.FindStringSubmatch(f.sms.Last().Body)[1]
	assert.NotEqual(t, first, second)
	_, err = f.s.ByToken(ctx, first)
	assert.ErrorIs(t, err, ErrNotFound, "the old link stops working")
	f.advance(ResendCooldown)
	require.NoError(t, f.s.Resend(ctx, f.agent, f.listing))
	f.advance(ResendCooldown)
	assert.Contains(t, f.s.Resend(ctx, f.agent, f.listing).(ValidationError)["form"], "3 times")

	require.NoError(t, f.s.Cancel(ctx, f.agent, f.listing))
	o, err := f.s.ByToken(ctx, linkRe.FindStringSubmatch(f.sms.Last().Body)[1])
	require.NoError(t, err)
	assert.Equal(t, Cancelled, o.State)
	_, err = f.s.Decide(ctx, linkRe.FindStringSubmatch(f.sms.Last().Body)[1], Approve, Visitor{})
	assert.Error(t, err, "a cancelled request can't be approved")

	// Pending requests lapse after two weeks.
	f.ask(t)
	f.advance(PendingTTL)
	m, _ := f.s.Latest(ctx, f.agent.UserID, f.prop)
	assert.Equal(t, Expired, StateOf(m, f.s.Now()))
	f.ask(t)

	// 3 + 1 + 1 sends so far today; the cap is 10.
	f.c.AgentMandate.Update().Where(agentmandate.AgentID(f.agent.UserID)).SetSends(5).ExecX(ctx)
	f.advance(PendingTTL)
	_, err = f.s.Ask(ctx, f.agent, f.listing, Request{LandlordPhone: "0201112222"})
	require.NoError(t, err, "yesterday's sends don't count")
	f.c.AgentMandate.Update().Where(agentmandate.AgentID(f.agent.UserID)).SetStatus(agentmandate.StatusCancelled).SetSends(10).ExecX(ctx)
	_, err = f.s.Ask(ctx, f.agent, f.listing, Request{LandlordPhone: "0201112222"})
	assert.Contains(t, err.(ValidationError)["form"], "Try again tomorrow")
}

func TestAskValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	_, err := f.s.Ask(ctx, f.agent, f.listing, Request{LandlordPhone: "12"})
	assert.Contains(t, err.(ValidationError)["landlord_phone"], "Ghana number")
	_, err = f.s.Ask(ctx, f.agent, f.listing, Request{LandlordPhone: "0244000001"})
	assert.Contains(t, err.(ValidationError)["landlord_phone"], "your own number")

	// Someone else's listing, and an owner's own listing.
	other := Actor{UserID: uuid.Must(uuid.NewV7())}
	_, err = f.s.Ask(ctx, other, f.listing, Request{LandlordPhone: "0201112222"})
	assert.ErrorIs(t, err, ErrNotFound)
	owner := f.c.User.Create().SetPhone("+233244000009").SaveX(ctx)
	p := f.c.Property.Create().SetCreatedBy(owner.ID).SaveX(ctx)
	u := f.c.Unit.Create().SetPropertyID(p.ID).SaveX(ctx)
	l := f.c.Listing.Create().SetUnitID(u.ID).SetListerID(owner.ID).SetListerKind(listing.ListerKindOwner).SaveX(ctx)
	_, err = f.s.Ask(ctx, Actor{UserID: owner.ID}, l.ID, Request{LandlordPhone: "0201112222"})
	assert.ErrorIs(t, err, ErrNoAgent)

	// A failed SMS leaves nothing behind.
	f.sms.Err = errors.New("provider down")
	_, err = f.s.Ask(ctx, f.agent, f.listing, Request{LandlordPhone: "0201112222"})
	assert.Contains(t, err.(ValidationError)["form"], "couldn't send")
	assert.Zero(t, f.c.AgentMandate.Query().CountX(ctx))
}
