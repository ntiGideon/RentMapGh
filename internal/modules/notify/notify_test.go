package notify

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/modules/messages"
	"rentmapgh/internal/platform/sms"
)

func TestNotify(t *testing.T) {
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

	capture := &sms.Capture{}
	hub := messages.NewHub()
	s := NewService(d.Ent, capture, hub)
	clock := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	u := d.Ent.User.Create().SetPhone("+233200000001").SetName("Akua").SaveX(ctx)
	events, cancel := hub.Subscribe(u.ID)
	defer cancel()

	s.Send(ctx, u.ID, Note{Topic: "viewings", Kind: "viewing.confirmed", Title: "Viewing confirmed", URL: "/viewings/x", SMS: "RentMap: confirmed"})
	n, _ := s.Unread(ctx, u.ID)
	assert.Equal(t, 1, n)
	assert.Equal(t, "RentMap: confirmed", capture.Last().Body)
	select {
	case e := <-events:
		assert.Equal(t, "notification", e.Kind)
		assert.Equal(t, 1, e.Unread)
	case <-time.After(time.Second):
		t.Fatal("no event")
	}

	// Preferences: viewings by SMS switched off → in-app only.
	d.Ent.User.UpdateOneID(u.ID).SetNotificationPrefs(map[string]bool{"viewings.sms": false}).ExecX(ctx)
	u = d.Ent.User.GetX(ctx, u.ID)
	s.SendTo(ctx, u, Note{Topic: "viewings", Kind: "k", Title: "Second", SMS: "RentMap: second"})
	assert.Len(t, capture.Messages(), 1)
	n, _ = s.Unread(ctx, u.ID)
	assert.Equal(t, 2, n)

	// Quiet hours hold non-urgent texts; urgent ones still go.
	clock = time.Date(2026, 10, 1, 22, 30, 0, 0, time.UTC)
	s.SendTo(ctx, u, Note{Topic: "messages", Kind: "k", Title: "Night", SMS: "RentMap: night"})
	assert.Len(t, capture.Messages(), 1)
	s.SendTo(ctx, u, Note{Topic: "messages", Kind: "k", Title: "Urgent", SMS: "RentMap: urgent", Urgent: true})
	assert.Equal(t, "RentMap: urgent", capture.Last().Body)

	ns, err := s.List(ctx, u.ID, 10)
	require.NoError(t, err)
	assert.Len(t, ns, 4)
	assert.Equal(t, "Urgent", ns[0].Title, "newest first")
	require.NoError(t, s.MarkAllRead(ctx, u.ID))
	n, _ = s.Unread(ctx, u.ID)
	assert.Zero(t, n)
}
