package verification

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/privacy"
	"rentmapgh/internal/ent/roleassignment"
	ev "rentmapgh/internal/ent/verification"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/imaging"
	"rentmapgh/internal/platform/sms"
	"rentmapgh/internal/platform/storage"
	"rentmapgh/internal/server/reqctx"
)

type fixture struct {
	svc   *Service
	db    *ent.Client
	mem   *storage.Memory
	sms   *sms.Capture
	clock *time.Time
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
	_, err = d.SQL.ExecContext(ctx, "TRUNCATE reports, messages, conversations, viewings, saved_listings, agent_mandates, listing_media, listing_terms, listings, units, properties, audit_events, sessions, role_assignments, otp_codes, verification_files, verifications, agent_profiles, landlord_profiles, users")
	require.NoError(t, err)

	mem := storage.NewMemory()
	sealed, err := storage.NewSealed(mem, make([]byte, 32))
	require.NoError(t, err)
	capture := &sms.Capture{}
	svc := NewService(d.Ent, sealed, audit.New(d.Ent), capture, "https://rentmap.test", 90*24*time.Hour)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }
	return &fixture{svc: svc, db: d.Ent, mem: mem, sms: capture, clock: &now}
}

func (f *fixture) user(t *testing.T, phone string, roles ...string) (*ent.User, context.Context) {
	t.Helper()
	ctx := context.Background()
	u, err := f.db.User.Create().SetPhone(phone).SetName("Test").Save(ctx)
	require.NoError(t, err)
	for _, r := range roles {
		require.NoError(t, f.db.RoleAssignment.Create().SetUserID(u.ID).SetRole(roleassignment.Role(r)).Exec(ctx))
	}
	return u, reqctx.WithViewer(ctx, &reqctx.Viewer{UserID: u.ID, Roles: roles, Phone: phone})
}

func photo(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 900, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 900; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func (f *fixture) submit(t *testing.T, ctx context.Context, u *ent.User) *ent.Verification {
	t.Helper()
	p := photo(t)
	v, err := f.svc.SubmitIdentity(ctx, Actor{UserID: u.ID}, IdentityInput{
		CardNumber: "gha 123456789 0", Front: p, Back: p, Selfie: p, Consent: true,
	})
	require.NoError(t, err)
	return v
}

func TestNormalizeCardNumber(t *testing.T) {
	for in, want := range map[string]string{
		"GHA-123456789-0": "GHA-123456789-0",
		"gha 123456789 0": "GHA-123456789-0",
		"1234567890":      "GHA-123456789-0",
		"GHA-12345678-0":  "",
		"GHA-123456789-X": "",
		"":                "",
	} {
		assert.Equal(t, want, NormalizeCardNumber(in), in)
	}
}

func TestSubmitValidation(t *testing.T) {
	f := setup(t)
	u, ctx := f.user(t, "+233241234567", "landlord")
	_, err := f.svc.SubmitIdentity(ctx, Actor{UserID: u.ID}, IdentityInput{CardNumber: "123", Front: []byte("not an image")})
	var verr ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Contains(t, verr, "card_number")
	assert.Contains(t, verr, "consent")
	assert.Contains(t, verr["id_front"], "JPEG, PNG or WebP")
	assert.Contains(t, verr, "id_back")
	assert.Contains(t, verr, "selfie")
	assert.Zero(t, f.mem.Len(), "nothing stored on a failed submission")
}

func TestIdentityFlowEndToEnd(t *testing.T) {
	f := setup(t)
	landlord, lctx := f.user(t, "+233241234567", "landlord")
	mod, mctx := f.user(t, "+233501234567", "moderator")

	v := f.submit(t, lctx, landlord)
	assert.Equal(t, ev.StatusPending, v.Status)
	assert.Equal(t, "7890", v.IDNumberLast4)
	assert.Equal(t, 3, f.mem.Len(), "three encrypted photos stored")

	// A second submission while pending is refused.
	p := photo(t)
	_, err := f.svc.SubmitIdentity(lctx, Actor{UserID: landlord.ID}, IdentityInput{CardNumber: "GHA-123456789-0", Front: p, Back: p, Selfie: p, Consent: true})
	assert.ErrorIs(t, err, ErrPending)

	// Owner can't review themselves (even if they were staff).
	assert.ErrorIs(t, f.svc.Decide(lctx, Actor{UserID: landlord.ID}, v.ID, Decision{Approve: true}), ErrSelfReview)

	// Moderator sees the queue and the decrypted card number.
	queue, err := f.svc.Queue(mctx, ev.StatusPending, 50)
	require.NoError(t, err)
	require.Len(t, queue, 1)
	r, err := f.svc.ForReview(mctx, Actor{UserID: mod.ID}, v.ID)
	require.NoError(t, err)
	assert.Equal(t, "GHA-123456789-0", r.CardNumber)
	require.Len(t, r.V.Edges.Files, 3)
	_, data, err := f.svc.File(mctx, v.ID, r.V.Edges.Files[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", imaging.Sniff(data))

	// Rejecting needs a reason; "other" needs a note.
	var verr ValidationError
	require.ErrorAs(t, f.svc.Decide(mctx, Actor{UserID: mod.ID}, v.ID, Decision{}), &verr)
	require.ErrorAs(t, f.svc.Decide(mctx, Actor{UserID: mod.ID}, v.ID, Decision{Reason: "other"}), &verr)

	require.NoError(t, f.svc.Decide(mctx, Actor{UserID: mod.ID}, v.ID, Decision{Approve: true}))
	assert.ErrorIs(t, f.svc.Decide(mctx, Actor{UserID: mod.ID}, v.ID, Decision{Approve: true}), ErrNotPending)

	u, err := f.db.User.Get(context.Background(), landlord.ID)
	require.NoError(t, err)
	assert.NotNil(t, u.IdentityVerifiedAt, "badge set")
	assert.Equal(t, "+233241234567", f.sms.Last().To)
	assert.Contains(t, f.sms.Last().Body, "identity is verified")

	st, err := f.svc.StatusFor(lctx, landlord.ID)
	require.NoError(t, err)
	assert.Equal(t, ev.StatusApproved, st.Identity.Status)
	_, err = f.svc.SubmitIdentity(lctx, Actor{UserID: landlord.ID}, IdentityInput{CardNumber: "GHA-123456789-0", Front: p, Back: p, Selfie: p, Consent: true})
	assert.ErrorIs(t, err, ErrAlreadyVerified)
}

func TestRejectionMessageAndResubmit(t *testing.T) {
	f := setup(t)
	landlord, lctx := f.user(t, "+233241234567", "landlord")
	mod, mctx := f.user(t, "+233501234567", "moderator")
	v := f.submit(t, lctx, landlord)
	require.NoError(t, f.svc.Decide(mctx, Actor{UserID: mod.ID}, v.ID, Decision{Reason: "unreadable"}))
	assert.Contains(t, f.sms.Last().Body, "blurry")

	st, err := f.svc.StatusFor(lctx, landlord.ID)
	require.NoError(t, err)
	assert.Equal(t, ev.StatusRejected, st.Identity.Status)
	assert.Contains(t, UserMessage(st.Identity), "Retake them")

	f.submit(t, lctx, landlord) // allowed again after a rejection
}

func TestPrivacyRules(t *testing.T) {
	f := setup(t)
	alice, actx := f.user(t, "+233241234567", "landlord")
	bob, bctx := f.user(t, "+233501234567", "landlord")
	v := f.submit(t, actx, alice)

	// Bob can't see Alice's verification or files, even by ID.
	_, err := f.db.Verification.Get(bctx, v.ID)
	assert.True(t, ent.IsNotFound(err), "filtered to owner: %v", err)
	n, err := f.db.VerificationFile.Query().Count(bctx)
	require.NoError(t, err)
	assert.Zero(t, n)

	// Anonymous queries are denied outright.
	_, err = f.db.Verification.Query().All(context.Background())
	assert.ErrorIs(t, err, privacy.Deny)

	// Bob can't create a verification in Alice's name, or approve his own.
	err = f.db.Verification.Create().SetUserID(alice.ID).SetKind(ev.KindIdentity).Exec(bctx)
	assert.ErrorIs(t, err, privacy.Deny)
	_, err = f.db.Verification.Update().Where(ev.UserID(bob.ID)).SetStatus(ev.StatusApproved).Save(bctx)
	assert.ErrorIs(t, err, privacy.Deny)

	// Alice sees her own.
	got, err := f.db.Verification.Get(actx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, v.ID, got.ID)
}

func TestPurgeEvidenceAfterRetention(t *testing.T) {
	f := setup(t)
	landlord, lctx := f.user(t, "+233241234567", "landlord")
	mod, mctx := f.user(t, "+233501234567", "moderator")
	v := f.submit(t, lctx, landlord)
	require.NoError(t, f.svc.Decide(mctx, Actor{UserID: mod.ID}, v.ID, Decision{Approve: true}))

	n, err := f.svc.PurgeEvidence(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "inside retention")
	assert.Equal(t, 3, f.mem.Len())

	*f.clock = f.clock.Add(91 * 24 * time.Hour)
	n, err = f.svc.PurgeEvidence(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Zero(t, f.mem.Len(), "photos deleted")

	sys := privacy.DecisionContext(context.Background(), privacy.Allow)
	got, err := f.db.Verification.Get(sys, v.ID)
	require.NoError(t, err)
	rows, err := f.db.QueryContext(sys, "SELECT id_number_enc IS NULL FROM verifications WHERE id = $1", v.ID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var isNull bool
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&isNull))
	assert.True(t, isNull, "card number deleted")
	assert.NotNil(t, got.EvidencePurgedAt)
	assert.Equal(t, ev.StatusApproved, got.Status, "decision kept")
}

func TestLicenceNeedsAgentRole(t *testing.T) {
	f := setup(t)
	agent, actx := f.user(t, "+233241234567", "agent")
	_, err := f.svc.SubmitLicense(actx, Actor{UserID: agent.ID}, []string{"renter"}, LicenseInput{Number: "REAC/1234"})
	assert.ErrorIs(t, err, ErrNotAgent)

	v, err := f.svc.SubmitLicense(actx, Actor{UserID: agent.ID}, []string{"agent"}, LicenseInput{Number: " reac/1234 ", AgencyName: "Adom Homes"})
	require.NoError(t, err)
	assert.Equal(t, "REAC/1234", v.LicenseNumber)
	p, err := f.db.AgentProfile.Query().Only(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "REAC/1234", p.LicenseNumber)
	assert.Equal(t, "Adom Homes", p.AgencyName)
}
