package trust

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/report"
	"rentmapgh/internal/ent/viewing"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/mandates"
	"rentmapgh/internal/modules/viewings"
	"rentmapgh/internal/platform/sms"
)

func TestTrustScore(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	require.NoError(t, db.Migrate(dsn))
	d, err := db.Open(ctx, dsn, 4)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	_, err = d.SQL.ExecContext(ctx, "TRUNCATE duplicate_candidates, notifications, reports, messages, conversations, viewings, saved_listings, agent_mandates, listing_media, listing_terms, listing_stats, listings, units, properties, audit_events, sessions, role_assignments, otp_codes, verification_files, verifications, agent_profiles, landlord_profiles, users")
	require.NoError(t, err)
	c := d.Ent
	vs := viewings.NewService(c, audit.New(c), &sms.Capture{}, "https://x")
	s := NewService(c, vs, mandates.NewService(c, audit.New(c), &sms.Capture{}, "secret-secret-secret-secret-secret", "https://x"))
	now := time.Now().UTC()

	// A brand-new owner with only a phone.
	u := c.User.Create().SetPhone("+233200000001").SetPhoneVerifiedAt(now).SaveX(ctx)
	p := c.Property.Create().SetCreatedBy(u.ID).SaveX(ctx)
	un := c.Unit.Create().SetPropertyID(p.ID).SaveX(ctx)
	l := c.Listing.Create().SetUnitID(un.ID).SetListerID(u.ID).SetListerKind(listing.ListerKindOwner).
		SetStatus(listing.StatusActive).SetLastConfirmedAt(now).SaveX(ctx)
	b, err := s.Refresh(ctx, l.ID)
	require.NoError(t, err)
	assert.Equal(t, WPhone+WAuthority+WFresh, b.Score)
	assert.Equal(t, b.Score, c.Listing.GetX(ctx, l.ID).TrustScore, "stored")

	// ID checked, and three accurate, well-attended viewings answered fast.
	c.User.UpdateOneID(u.ID).SetIdentityVerifiedAt(now).ExecX(ctx)
	r := c.User.Create().SetPhone("+233200000002").SaveX(ctx)
	for i := range 3 {
		c.Viewing.Create().SetListingID(l.ID).SetRenterID(r.ID).SetListerID(u.ID).SetStatus(viewing.StatusCompleted).
			SetStartsAt(now.Add(-time.Duration(48+i) * time.Hour)).SetCreatedAt(now.Add(-100 * time.Hour)).
			SetRespondedAt(now.Add(-99 * time.Hour)).SetAccuracy(viewing.AccuracyAsDescribed).ExecX(ctx)
	}
	b, _ = s.Refresh(ctx, l.ID)
	assert.True(t, b.Identity && b.RepliesFast && b.ShowsUp && b.Accurate)
	assert.Equal(t, WIdentity+WPhone+WAuthority+WRepliesFast+WShowsUp+WAccurate+WFresh, b.Score)

	// An upheld report costs points.
	c.Report.Create().SetReporterID(r.ID).SetTargetType(report.TargetTypeListing).SetTargetID(l.ID).SetSubjectID(u.ID).
		SetReason("scam").SetStatus(report.StatusActioned).ExecX(ctx)
	b2, _ := s.Refresh(ctx, l.ID)
	assert.Equal(t, b.Score+WReportUpheld, b2.Score)

	n, err := s.RefreshAll(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing changed since")
}
