// Package verification runs identity (Ghana Card + selfie) and agent-licence
// verification: users submit evidence, moderators review it in the admin
// queue, and the outcome becomes a badge.
//
// v1 is manual review (ProjectRequirement §6.6 fallback). A KYC provider such
// as Smile ID can later fill Method = "smile_id" and decide automatically.
package verification

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/agentprofile"
	"rentmapgh/internal/ent/privacy"
	"rentmapgh/internal/ent/user"
	ev "rentmapgh/internal/ent/verification"
	"rentmapgh/internal/ent/verificationfile"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/imaging"
	"rentmapgh/internal/platform/sms"
	"rentmapgh/internal/platform/storage"
)

// Audit actions.
const (
	actSubmitted = "verification.submitted"
	actApproved  = "verification.approved"
	actRejected  = "verification.rejected"
	actViewed    = "verification.evidence_viewed"
	actPurged    = "verification.evidence_purged"
)

// Upload limits.
const (
	MaxFileBytes = 12 << 20 // before client-side compression, phone photos can be big
	maxDim       = 2000     // stored photos are downscaled to this
	minDim       = 400      // smaller than this can't be read reliably
)

var (
	ErrPending         = errors.New("You already have a verification under review. We'll text you when it's done.")
	ErrAlreadyVerified = errors.New("You're already verified.")
	ErrNotPending      = errors.New("This verification has already been decided.")
	ErrSelfReview      = errors.New("You can't review your own verification.")
	ErrNotAgent        = errors.New("Add the agent role to your account first.")
)

// ValidationError maps form field → message.
type ValidationError map[string]string

func (v ValidationError) Error() string {
	return fmt.Sprintf("verification: %d invalid field(s)", len(v))
}

// Reason is a rejection reason moderators pick from.
type Reason struct{ Code, Label, Message string }

// Reasons are shown to moderators as a list and to users as Message.
var Reasons = map[string][]Reason{
	"identity": {
		{"unreadable", "Photos unreadable", "The photos were blurry, dark or cut off. Retake them in good light with the whole card visible."},
		{"selfie_mismatch", "Selfie doesn't match", "Your selfie didn't clearly match the photo on your Ghana Card."},
		{"number_mismatch", "Card number mismatch", "The card number you typed didn't match the card in the photos."},
		{"not_ghana_card", "Not a Ghana Card", "We can only accept the Ghana Card (National ID) for now."},
		{"expired", "Card expired", "The card in the photos has expired."},
		{"other", "Other (explain)", ""},
	},
	"license": {
		{"not_found", "Not in the register", "We couldn't find this licence number in the Real Estate Agency Council register."},
		{"name_mismatch", "Name mismatch", "The name on the licence didn't match your verified identity."},
		{"expired", "Licence expired", "This licence has expired. Renew it with the Real Estate Agency Council and try again."},
		{"other", "Other (explain)", ""},
	},
}

func reasonFor(kind, code string) (Reason, bool) {
	for _, r := range Reasons[kind] {
		if r.Code == code {
			return r, true
		}
	}
	return Reason{}, false
}

// Actor is who is acting, for audit and self-review checks.
type Actor struct {
	UserID    uuid.UUID
	IP        string
	UserAgent string
}

type Service struct {
	db        *ent.Client
	evidence  *storage.Sealed
	audit     *audit.Log
	sms       sms.Sender
	baseURL   string
	retention time.Duration
	now       func() time.Time
}

func NewService(db *ent.Client, evidence *storage.Sealed, log *audit.Log, sender sms.Sender, baseURL string, retention time.Duration) *Service {
	return &Service{db: db, evidence: evidence, audit: log, sms: sender, baseURL: baseURL, retention: retention,
		now: func() time.Time { return time.Now().UTC() }}
}

// RetentionDays is the evidence retention period, for user-facing copy.
func (s *Service) RetentionDays() int { return int(s.retention.Hours() / 24) }

// ── Submitting ────────────────────────────────────────────────────────────

type IdentityInput struct {
	CardNumber          string
	Front, Back, Selfie []byte
	Consent             bool
}

var cardDigits = regexp.MustCompile(`^\d{10}$`)

// NormalizeCardNumber turns "gha 123456789 0" or "GHA-1234567890" into
// "GHA-123456789-0" (the Ghana Card PIN format), or returns "".
func NormalizeCardNumber(raw string) string {
	s := strings.ToUpper(raw)
	s = strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return -1
	}, s)
	s = strings.TrimPrefix(s, "GHA")
	if !cardDigits.MatchString(s) {
		return ""
	}
	return "GHA-" + s[:9] + "-" + s[9:]
}

// SubmitIdentity stores Ghana Card photos + selfie for manual review.
func (s *Service) SubmitIdentity(ctx context.Context, a Actor, in IdentityInput) (*ent.Verification, error) {
	errs := ValidationError{}
	card := NormalizeCardNumber(in.CardNumber)
	if card == "" {
		errs["card_number"] = "Enter the number printed on your card, like GHA-123456789-0."
	}
	if !in.Consent {
		errs["consent"] = "Please agree so we can check your ID."
	}
	photos := map[string][]byte{"id_front": in.Front, "id_back": in.Back, "selfie": in.Selfie}
	labels := map[string]string{"id_front": "front of your card", "id_back": "back of your card", "selfie": "selfie"}
	clean := map[string][]byte{}
	for _, kind := range []string{"id_front", "id_back", "selfie"} {
		raw := photos[kind]
		if len(raw) == 0 {
			errs[kind] = "Add a photo of the " + labels[kind] + "."
			continue
		}
		out, err := imaging.Normalize(raw, maxDim, minDim)
		if err != nil {
			errs[kind] = userMessage(err)
			continue
		}
		clean[kind] = out
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if err := s.checkOpen(ctx, a.UserID, ev.KindIdentity); err != nil {
		return nil, err
	}

	digits := strings.ReplaceAll(strings.TrimPrefix(card, "GHA-"), "-", "")
	return s.create(ctx, a, ev.KindIdentity, func(c *ent.VerificationCreate, id uuid.UUID) error {
		enc, err := s.evidence.Seal("verification/"+id.String()+"/id_number", []byte(card))
		if err != nil {
			return err
		}
		c.SetIDNumberEnc(enc).SetIDNumberLast4(digits[len(digits)-4:])
		return nil
	}, clean)
}

type LicenseInput struct {
	Number     string
	AgencyName string
	Document   []byte // optional photo of the licence certificate
}

var licenceRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9/ -]{2,38}[A-Z0-9]$`)

// SubmitLicense records an agent's licence number (and optional certificate
// photo) for review, and saves it on their agent profile.
func (s *Service) SubmitLicense(ctx context.Context, a Actor, roles []string, in LicenseInput) (*ent.Verification, error) {
	if !contains(roles, "agent") {
		return nil, ErrNotAgent
	}
	errs := ValidationError{}
	number := strings.Join(strings.Fields(strings.ToUpper(in.Number)), " ")
	if !licenceRe.MatchString(number) {
		errs["license_number"] = "Enter your licence number exactly as it appears on your certificate."
	}
	agency := strings.Join(strings.Fields(in.AgencyName), " ")
	if len(agency) > 120 {
		errs["agency_name"] = "Please keep the agency name under 120 characters."
	}
	clean := map[string][]byte{}
	if len(in.Document) > 0 {
		out, err := imaging.Normalize(in.Document, maxDim, minDim)
		if err != nil {
			errs["license_doc"] = userMessage(err)
		} else {
			clean["license_doc"] = out
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if err := s.checkOpen(ctx, a.UserID, ev.KindLicense); err != nil {
		return nil, err
	}

	// The agent profile is the user's own record: keep it in step.
	if err := s.db.AgentProfile.Create().SetUserID(a.UserID).SetLicenseNumber(number).SetAgencyName(agency).
		OnConflictColumns(agentprofile.FieldUserID).
		Update(func(u *ent.AgentProfileUpsert) { u.SetLicenseNumber(number).SetAgencyName(agency).UpdateUpdatedAt() }).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("license: agent profile: %w", err)
	}
	return s.create(ctx, a, ev.KindLicense, func(c *ent.VerificationCreate, _ uuid.UUID) error {
		c.SetLicenseNumber(number)
		return nil
	}, clean)
}

// checkOpen refuses a new submission while one is pending or approved.
func (s *Service) checkOpen(ctx context.Context, userID uuid.UUID, kind ev.Kind) error {
	open, err := s.db.Verification.Query().
		Where(ev.UserID(userID), ev.KindEQ(kind), ev.StatusIn(ev.StatusPending, ev.StatusApproved)).
		Select(ev.FieldStatus).All(ctx)
	if err != nil {
		return fmt.Errorf("verification: check open: %w", err)
	}
	if slices.ContainsFunc(open, func(v *ent.Verification) bool { return v.Status == ev.StatusPending }) {
		return ErrPending
	}
	if len(open) > 0 {
		return ErrAlreadyVerified
	}
	return nil
}

// create writes the verification, its encrypted files and the audit record.
// Files go to storage first; if the database step fails they're removed.
func (s *Service) create(ctx context.Context, a Actor, kind ev.Kind, fill func(*ent.VerificationCreate, uuid.UUID) error,
	files map[string][]byte,
) (*ent.Verification, error) {
	id := uuid.Must(uuid.NewV7())
	var written []string
	cleanup := func() {
		for _, k := range written {
			_ = s.evidence.Delete(context.WithoutCancel(ctx), k)
		}
	}

	type fileRow struct {
		kind, key string
		data      []byte
	}
	var rows []fileRow
	for fk, data := range files {
		key := "evidence/" + id.String() + "/" + fk + "-" + randHex(6) + ".jpg"
		if err := s.evidence.Put(ctx, key, data); err != nil {
			cleanup()
			return nil, fmt.Errorf("verification: store %s: %w", fk, err)
		}
		written = append(written, key)
		rows = append(rows, fileRow{fk, key, data})
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("verification: begin: %w", err)
	}
	c := tx.Verification.Create().SetID(id).SetUserID(a.UserID).SetKind(kind)
	if err := fill(c, id); err != nil {
		_ = tx.Rollback()
		cleanup()
		return nil, err
	}
	v, err := c.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		cleanup()
		if ent.IsConstraintError(err) { // a parallel submission won
			return nil, ErrPending
		}
		return nil, fmt.Errorf("verification: create: %w", err)
	}
	for _, r := range rows {
		sum := sha256.Sum256(r.data)
		if err := tx.VerificationFile.Create().SetVerificationID(id).SetUserID(a.UserID).
			SetKind(verificationfile.Kind(r.kind)).SetStorageKey(r.key).SetContentType("image/jpeg").
			SetSize(len(r.data)).SetSha256(sum[:]).Exec(ctx); err != nil {
			_ = tx.Rollback()
			cleanup()
			return nil, fmt.Errorf("verification: file row: %w", err)
		}
	}
	if err := audit.RecordTx(ctx, tx, audit.Event{Actor: &a.UserID, Action: actSubmitted, TargetType: "verification",
		TargetID: id.String(), IP: a.IP, UserAgent: a.UserAgent, Meta: map[string]any{"kind": string(kind), "files": len(rows)}}); err != nil {
		_ = tx.Rollback()
		cleanup()
		return nil, fmt.Errorf("verification: audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		cleanup()
		return nil, fmt.Errorf("verification: commit: %w", err)
	}
	return v, nil
}

// ── Status for the user ───────────────────────────────────────────────────

// Status is the latest verification of each kind for a user (nil if none).
type Status struct {
	Identity *ent.Verification
	License  *ent.Verification
}

func (s *Service) StatusFor(ctx context.Context, userID uuid.UUID) (Status, error) {
	all, err := s.db.Verification.Query().Where(ev.UserID(userID)).
		Order(ent.Desc(ev.FieldCreatedAt)).Limit(20).All(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("verification: status: %w", err)
	}
	var st Status
	for _, v := range all {
		switch {
		case v.Kind == ev.KindIdentity && st.Identity == nil:
			st.Identity = v
		case v.Kind == ev.KindLicense && st.License == nil:
			st.License = v
		}
	}
	return st, nil
}

// UserMessage is what the user sees for a rejected verification.
func UserMessage(v *ent.Verification) string {
	if v.DecisionNote != "" {
		return v.DecisionNote
	}
	if r, ok := reasonFor(string(v.Kind), v.DecisionReason); ok {
		return r.Message
	}
	return "We couldn't complete this verification."
}

// ── Moderation ────────────────────────────────────────────────────────────

// Queue lists verifications for moderators, oldest first for pending.
func (s *Service) Queue(ctx context.Context, status ev.Status, limit int) ([]*ent.Verification, error) {
	q := s.db.Verification.Query().Where(ev.StatusEQ(status)).WithUser().Limit(limit)
	if status == ev.StatusPending {
		q = q.Order(ent.Asc(ev.FieldCreatedAt))
	} else {
		q = q.Order(ent.Desc(ev.FieldReviewedAt))
	}
	return q.All(ctx)
}

// Counts per status, for the queue tabs.
func (s *Service) Counts(ctx context.Context) (map[ev.Status]int, error) {
	var rows []struct {
		Status ev.Status `json:"status"`
		Count  int       `json:"count"`
	}
	if err := s.db.Verification.Query().GroupBy(ev.FieldStatus).Aggregate(ent.Count()).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("verification: counts: %w", err)
	}
	out := map[ev.Status]int{}
	for _, r := range rows {
		out[r.Status] = r.Count
	}
	return out, nil
}

// Review is everything a moderator needs to decide.
type Review struct {
	V          *ent.Verification
	User       *ent.User
	CardNumber string // decrypted; "" when purged or not identity
	History    []*ent.Verification
}

// ForReview loads one verification with its user, files and history, and
// audits the access (ID evidence is personal data under Act 843).
func (s *Service) ForReview(ctx context.Context, reviewer Actor, id uuid.UUID) (*Review, error) {
	v, err := s.db.Verification.Query().Where(ev.ID(id)).
		WithFiles().
		WithUser(func(q *ent.UserQuery) { q.WithRoles().WithAgentProfile().WithLandlordProfile() }).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	// Front, back, selfie: the order a moderator compares them in.
	order := map[verificationfile.Kind]int{"id_front": 0, "id_back": 1, "selfie": 2, "license_doc": 3}
	slices.SortFunc(v.Edges.Files, func(a, b *ent.VerificationFile) int { return order[a.Kind] - order[b.Kind] })
	r := &Review{V: v, User: v.Edges.User}
	if v.IDNumberEnc != nil && len(*v.IDNumberEnc) > 0 { // NULL scans as a pointer to an empty slice
		plain, err := s.evidence.Open("verification/"+v.ID.String()+"/id_number", *v.IDNumberEnc)
		if err != nil {
			slog.ErrorContext(ctx, "verification: decrypt card number", "id", v.ID, "err", err)
		} else {
			r.CardNumber = string(plain)
		}
	}
	r.History, err = s.db.Verification.Query().Where(ev.UserID(v.UserID), ev.IDNEQ(v.ID)).
		Order(ent.Desc(ev.FieldCreatedAt)).Limit(10).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("verification: history: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &reviewer.UserID, Action: actViewed, TargetType: "verification",
		TargetID: v.ID.String(), IP: reviewer.IP, UserAgent: reviewer.UserAgent})
	return r, nil
}

// File returns one decrypted evidence file.
func (s *Service) File(ctx context.Context, verificationID, fileID uuid.UUID) (*ent.VerificationFile, []byte, error) {
	f, err := s.db.VerificationFile.Query().
		Where(verificationfile.ID(fileID), verificationfile.VerificationID(verificationID)).Only(ctx)
	if err != nil {
		return nil, nil, err
	}
	data, err := s.evidence.Get(ctx, f.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return f, data, nil
}

type Decision struct {
	Approve bool
	Reason  string // code, required when rejecting
	Note    string // free text for the user; required for "other"
}

// Decide approves or rejects a pending verification and notifies the user.
func (s *Service) Decide(ctx context.Context, reviewer Actor, id uuid.UUID, d Decision) error {
	v, err := s.db.Verification.Query().Where(ev.ID(id)).WithUser().Only(ctx)
	if err != nil {
		return err
	}
	if v.UserID == reviewer.UserID {
		return ErrSelfReview
	}
	if v.Status != ev.StatusPending || !UserIsActive(v.Edges.User) {
		return ErrNotPending
	}
	d.Note = strings.TrimSpace(d.Note)
	if len(d.Note) > 500 {
		return ValidationError{"note": "Keep the note under 500 characters."}
	}
	if !d.Approve {
		r, ok := reasonFor(string(v.Kind), d.Reason)
		if !ok {
			return ValidationError{"reason": "Choose why you're rejecting this."}
		}
		if r.Code == "other" && d.Note == "" {
			return ValidationError{"note": "Explain what the user needs to fix."}
		}
	}

	now := s.now()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("decide: begin: %w", err)
	}
	status, action := ev.StatusRejected, actRejected
	if d.Approve {
		status, action = ev.StatusApproved, actApproved
	}
	n, err := tx.Verification.Update().Where(ev.ID(id), ev.StatusEQ(ev.StatusPending)).
		SetStatus(status).SetReviewedBy(reviewer.UserID).SetReviewedAt(now).
		SetDecisionReason(d.Reason).SetDecisionNote(d.Note).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("decide: update: %w", err)
	}
	if n == 0 { // another moderator got there first
		_ = tx.Rollback()
		return ErrNotPending
	}
	if d.Approve {
		u := tx.User.UpdateOneID(v.UserID)
		if v.Kind == ev.KindIdentity {
			u.SetIdentityVerifiedAt(now)
		} else {
			u.SetLicenseVerifiedAt(now)
		}
		if err := u.Exec(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("decide: badge: %w", err)
		}
	}
	if err := audit.RecordTx(ctx, tx, audit.Event{Actor: &reviewer.UserID, Action: action, TargetType: "verification",
		TargetID: id.String(), IP: reviewer.IP, UserAgent: reviewer.UserAgent,
		Meta: map[string]any{"kind": string(v.Kind), "reason": d.Reason, "subject": v.UserID.String()}}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("decide: audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("decide: commit: %w", err)
	}

	if u := v.Edges.User; u != nil && u.Phone != nil {
		v.DecisionReason, v.DecisionNote = d.Reason, d.Note
		msg := s.decisionSMS(v, d.Approve)
		sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
		defer cancel()
		if err := s.sms.Send(sendCtx, sms.Message{To: *u.Phone, Body: msg}); err != nil {
			slog.WarnContext(ctx, "verification: decision sms", "err", err) // the account page shows the result anyway
		}
	}
	return nil
}

func (s *Service) decisionSMS(v *ent.Verification, approved bool) string {
	what := "identity"
	if v.Kind == ev.KindLicense {
		what = "agent licence"
	}
	if approved {
		return "RentMap: your " + what + " is verified. Your profile now shows the badge."
	}
	return "RentMap: we couldn't verify your " + what + ". " + UserMessage(v) + " Try again: " + s.baseURL + "/account"
}

// ── Retention ─────────────────────────────────────────────────────────────

// PurgeEvidence deletes photos and card numbers of verifications decided
// longer ago than the retention period. The decision itself is kept.
func (s *Service) PurgeEvidence(ctx context.Context) (int, error) {
	ctx = privacy.DecisionContext(ctx, privacy.Allow) // system job, no viewer
	cutoff := s.now().Add(-s.retention)
	due, err := s.db.Verification.Query().
		Where(ev.StatusIn(ev.StatusApproved, ev.StatusRejected, ev.StatusWithdrawn), ev.EvidencePurgedAtIsNil(),
			ev.Or(ev.ReviewedAtLT(cutoff), ev.And(ev.ReviewedAtIsNil(), ev.UpdatedAtLT(cutoff)))).
		Limit(200).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge: query: %w", err)
	}
	for _, v := range due {
		if err := s.purgeOne(ctx, v.ID); err != nil {
			return 0, err
		}
		s.audit.Record(ctx, audit.Event{Action: actPurged, TargetType: "verification", TargetID: v.ID.String()})
	}
	return len(due), nil
}

// PurgeUser removes all evidence of a user (account deletion) and withdraws
// open reviews.
func (s *Service) PurgeUser(ctx context.Context, userID uuid.UUID) error {
	ctx = privacy.DecisionContext(ctx, privacy.Allow)
	if _, err := s.db.Verification.Update().Where(ev.UserID(userID), ev.StatusEQ(ev.StatusPending)).
		SetStatus(ev.StatusWithdrawn).Save(ctx); err != nil {
		return fmt.Errorf("purge user: withdraw: %w", err)
	}
	ids, err := s.db.Verification.Query().Where(ev.UserID(userID), ev.EvidencePurgedAtIsNil()).IDs(ctx)
	if err != nil {
		return fmt.Errorf("purge user: query: %w", err)
	}
	for _, id := range ids {
		if err := s.purgeOne(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) purgeOne(ctx context.Context, id uuid.UUID) error {
	files, err := s.db.VerificationFile.Query().Where(verificationfile.VerificationID(id)).All(ctx)
	if err != nil {
		return fmt.Errorf("purge: files: %w", err)
	}
	for _, f := range files {
		if err := s.evidence.Delete(ctx, f.StorageKey); err != nil {
			return fmt.Errorf("purge: delete %s: %w", f.StorageKey, err)
		}
	}
	if _, err := s.db.VerificationFile.Delete().Where(verificationfile.VerificationID(id)).Exec(ctx); err != nil {
		return fmt.Errorf("purge: file rows: %w", err)
	}
	return s.db.Verification.UpdateOneID(id).ClearIDNumberEnc().SetEvidencePurgedAt(s.now()).Exec(ctx)
}

// ── helpers ───────────────────────────────────────────────────────────────

// UserIsActive guards against reviewing deleted accounts.
func UserIsActive(u *ent.User) bool { return u != nil && u.Status == user.StatusActive }

func userMessage(err error) string {
	for _, e := range []error{imaging.ErrUnsupported, imaging.ErrTooSmall, imaging.ErrCorrupt} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return imaging.ErrCorrupt.Error()
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
