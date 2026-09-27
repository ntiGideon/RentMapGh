// Package messages lets renters and listers talk about a listing
// (ProjectRequirement §6.9): one conversation per renter and listing,
// realtime updates over SSE, read receipts, unread badges, scam-shield
// warnings and reports. Phone numbers in messages stay hidden until the two
// have a confirmed viewing.
package messages

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/conversation"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/message"
	"rentmapgh/internal/ent/report"
	"rentmapgh/internal/ent/viewing"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/sms"
)

const (
	MaxBody         = 2000
	MaxNewPerDay    = 20            // new conversations a renter may start
	MaxPerMinute    = 20            // messages per user per minute
	NotifyEvery     = 6 * time.Hour // at most one "unread messages" SMS per side
	ReportNoteLimit = 500
)

// ReportReasons are the choices on the report form.
var ReportReasons = []struct{ Key, Label string }{
	{"scam", "Asking for money or looks like a scam"},
	{"harassment", "Rude, threatening or harassing"},
	{"spam", "Spam or advertising"},
	{"fake", "The place isn't real or isn't theirs"},
	{"other", "Something else"},
}

var (
	ErrNotFound  = errors.New("messages: not found")
	ErrForbidden = errors.New("messages: that's your own listing")
)

// ValidationError maps field → message.
type ValidationError map[string]string

func (v ValidationError) Error() string { return fmt.Sprintf("messages: %d invalid field(s)", len(v)) }

// Actor is the signed-in user.
type Actor struct {
	UserID        uuid.UUID
	IP, UserAgent string
}

type Service struct {
	onContact func(ctx context.Context, listingID uuid.UUID) // a renter got in touch (stats)
	db        *ent.Client
	audit     *audit.Log
	sms       sms.Sender
	hub       *Hub
	baseURL   string
	now       func() time.Time
}

func NewService(db *ent.Client, log *audit.Log, sender sms.Sender, hub *Hub, baseURL string) *Service {
	return &Service{db: db, audit: log, sms: sender, hub: hub, baseURL: strings.TrimRight(baseURL, "/"),
		now: func() time.Time { return time.Now().UTC() }}
}

// OnContact registers a hook for new conversations (listing stats).
func (s *Service) OnContact(f func(ctx context.Context, listingID uuid.UUID)) { s.onContact = f }

// Hub is the event hub (the stream endpoint subscribes to it).
func (s *Service) Hub() *Hub { return s.hub }

// ── Conversations ────────────────────────────────────────────────────────

// Start opens (or finds) the renter's conversation about a live listing.
func (s *Service) Start(ctx context.Context, a Actor, listingID uuid.UUID) (*ent.Conversation, error) {
	l, err := s.db.Listing.Get(ctx, listingID)
	if ent.IsNotFound(err) || (err == nil && l.Status != listing.StatusActive) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("messages: listing: %w", err)
	}
	if l.ListerID == a.UserID {
		return nil, ErrForbidden
	}
	c, err := s.db.Conversation.Query().Where(conversation.ListingID(listingID), conversation.RenterID(a.UserID)).Only(ctx)
	if err == nil {
		return c, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("messages: find: %w", err)
	}
	n, err := s.db.Conversation.Query().Where(conversation.RenterID(a.UserID), conversation.CreatedAtGT(s.now().Add(-24*time.Hour))).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("messages: count: %w", err)
	}
	if n >= MaxNewPerDay {
		return nil, ValidationError{"form": "You've started a lot of conversations today. Try again tomorrow."}
	}
	c, err = s.db.Conversation.Create().SetListingID(listingID).SetRenterID(a.UserID).SetListerID(l.ListerID).Save(ctx)
	if ent.IsConstraintError(err) { // started in another tab a moment ago
		return s.db.Conversation.Query().Where(conversation.ListingID(listingID), conversation.RenterID(a.UserID)).Only(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("messages: create: %w", err)
	}
	if s.onContact != nil {
		s.onContact(ctx, listingID)
	}
	return c, nil
}

// Thread is a conversation as one participant sees it.
type Thread struct {
	C        *ent.Conversation
	Listing  *ent.Listing
	Me, Them *ent.User
	Role     string // "renter" or "lister"
	Unlocked bool   // a confirmed viewing: phone numbers may show
}

// TheirReadAt is when the other side last read the thread.
func (t *Thread) TheirReadAt() *time.Time {
	if t.Role == "renter" {
		return t.C.ListerReadAt
	}
	return t.C.RenterReadAt
}

// Load returns a conversation for one of its two participants.
func (s *Service) Load(ctx context.Context, a Actor, id uuid.UUID) (*Thread, error) {
	c, err := s.db.Conversation.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("messages: load: %w", err)
	}
	t := &Thread{C: c}
	themID := c.ListerID
	switch a.UserID {
	case c.RenterID:
		t.Role = "renter"
	case c.ListerID:
		t.Role, themID = "lister", c.RenterID
	default:
		return nil, ErrNotFound
	}
	if t.Me, err = s.db.User.Get(ctx, a.UserID); err != nil {
		return nil, fmt.Errorf("messages: me: %w", err)
	}
	if t.Them, err = s.db.User.Get(ctx, themID); err != nil {
		return nil, fmt.Errorf("messages: them: %w", err)
	}
	if t.Listing, err = s.db.Listing.Get(ctx, c.ListingID); err != nil {
		return nil, fmt.Errorf("messages: listing: %w", err)
	}
	t.Unlocked, err = s.db.Viewing.Query().Where(viewing.ListingID(c.ListingID), viewing.RenterID(c.RenterID),
		viewing.StatusIn(viewing.StatusConfirmed, viewing.StatusCompleted)).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("messages: unlocked: %w", err)
	}
	return t, nil
}

// Messages returns the thread's messages after `after` (all if Nil),
// oldest first, at most 200.
func (s *Service) Messages(ctx context.Context, t *Thread, after uuid.UUID) ([]*ent.Message, error) {
	q := s.db.Message.Query().Where(message.ConversationID(t.C.ID))
	if after != uuid.Nil {
		q.Where(message.IDGT(after)) // UUIDv7: IDs sort by time
	}
	ms, err := q.Order(ent.Asc(message.FieldID)).Limit(200).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("messages: list: %w", err)
	}
	return ms, nil
}

// Read marks the thread read by the actor and tells the other side (read
// receipts).
func (s *Service) Read(ctx context.Context, t *Thread) {
	now := s.now()
	up := s.db.Conversation.UpdateOne(t.C)
	if t.Role == "renter" {
		up.SetRenterReadAt(now)
	} else {
		up.SetListerReadAt(now)
	}
	c, err := up.Save(ctx)
	if err != nil {
		slog.WarnContext(ctx, "messages: read", "err", err)
		return
	}
	t.C = c
	s.hub.Publish(t.Them.ID, Event{Kind: "read", Conversation: c.ID}) // their read receipt
	s.publishUnread(ctx, t.Me.ID, c.ID, "unread")                     // my badge, in my other tabs
}

// Send posts a message and notifies the other side.
func (s *Service) Send(ctx context.Context, a Actor, t *Thread, body string) (*ent.Message, error) {
	body = strings.TrimSpace(body)
	switch {
	case body == "":
		return nil, ValidationError{"body": "Write a message first."}
	case utf8.RuneCountInString(body) > MaxBody:
		return nil, ValidationError{"body": fmt.Sprintf("Keep it under %d characters.", MaxBody)}
	}
	recent, err := s.db.Message.Query().Where(message.SenderID(a.UserID), message.CreatedAtGT(s.now().Add(-time.Minute))).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("messages: rate: %w", err)
	}
	if recent >= MaxPerMinute {
		return nil, ValidationError{"body": "You're sending messages very fast. Wait a moment."}
	}
	flags := Check(body)
	if HasPhone(body) && !t.Unlocked {
		flags = append(flags, "phone")
	}
	m, err := s.db.Message.Create().SetConversationID(t.C.ID).SetSenderID(a.UserID).SetBody(body).SetFlags(flags).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("messages: send: %w", err)
	}
	now := m.CreatedAt
	up := s.db.Conversation.UpdateOne(t.C).SetLastMessageAt(now).SetLastSenderID(a.UserID)
	if t.Role == "renter" {
		up.SetRenterReadAt(now)
	} else {
		up.SetListerReadAt(now)
	}
	if t.C, err = up.Save(ctx); err != nil {
		return nil, fmt.Errorf("messages: touch: %w", err)
	}
	if len(flags) > 0 {
		s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: "message.flagged", TargetType: "message", TargetID: m.ID.String(),
			IP: a.IP, UserAgent: a.UserAgent, Meta: map[string]any{"flags": flags, "conversation": t.C.ID.String()}})
	}
	s.publishUnread(ctx, t.Them.ID, t.C.ID, "message")
	s.publishUnread(ctx, t.Me.ID, t.C.ID, "message") // the sender's other tabs
	s.maybeText(ctx, t)
	return m, nil
}

// maybeText sends one SMS about unread messages per NotifyEvery, so people
// who aren't on the site still hear about replies.
func (s *Service) maybeText(ctx context.Context, t *Thread) {
	now := s.now()
	last, set := t.C.ListerNotifiedAt, s.db.Conversation.UpdateOne(t.C).SetListerNotifiedAt(now)
	if t.Role == "lister" {
		last, set = t.C.RenterNotifiedAt, s.db.Conversation.UpdateOne(t.C).SetRenterNotifiedAt(now)
	}
	if last != nil && now.Sub(*last) < NotifyEvery {
		return
	}
	if t.Them.Phone == nil {
		return
	}
	if err := set.Exec(ctx); err != nil {
		slog.WarnContext(ctx, "messages: notified", "err", err)
		return
	}
	who := "A renter"
	if t.Role == "lister" {
		who = "The lister"
	}
	if name, _, _ := strings.Cut(t.Me.Name, " "); name != "" {
		who = name
	}
	about := "your listing"
	if t.Listing.Headline != "" {
		h := []rune(t.Listing.Headline)
		if len(h) > 40 {
			h = append(h[:39], '…')
		}
		about = "\"" + string(h) + "\""
	}
	body := "RentMap: " + who + " sent you a message about " + about + ". Reply: " + s.baseURL + "/messages/" + t.C.ID.String()
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
	defer cancel()
	if err := s.sms.Send(sendCtx, sms.Message{To: *t.Them.Phone, Body: body}); err != nil {
		slog.WarnContext(ctx, "messages: sms", "err", err)
	}
}

func (s *Service) publishUnread(ctx context.Context, user, conv uuid.UUID, kind string) {
	n, err := s.Unread(ctx, user)
	if err != nil {
		slog.WarnContext(ctx, "messages: unread", "err", err)
	}
	s.hub.Publish(user, Event{Kind: kind, Conversation: conv, Unread: n})
}

// Unread counts conversations with a message the user hasn't read.
func (s *Service) Unread(ctx context.Context, user uuid.UUID) (int, error) {
	cs, err := s.db.Conversation.Query().Where(
		conversation.Or(conversation.RenterID(user), conversation.ListerID(user)),
		conversation.LastSenderIDNEQ(user), conversation.LastMessageAtNotNil()).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("messages: unread: %w", err)
	}
	n := 0
	for _, c := range cs {
		read := c.ListerReadAt
		if c.RenterID == user {
			read = c.RenterReadAt
		}
		if read == nil || read.Before(*c.LastMessageAt) {
			n++
		}
	}
	return n, nil
}

// InboxItem is one row of the inbox.
type InboxItem struct {
	C       *ent.Conversation
	Listing *ent.Listing
	Them    *ent.User
	Last    *ent.Message
	Unread  bool
}

// Inbox lists the user's conversations, most recent first.
func (s *Service) Inbox(ctx context.Context, user uuid.UUID) ([]InboxItem, error) {
	cs, err := s.db.Conversation.Query().Where(conversation.Or(conversation.RenterID(user), conversation.ListerID(user)),
		conversation.LastMessageAtNotNil()).Order(ent.Desc(conversation.FieldLastMessageAt)).Limit(100).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("messages: inbox: %w", err)
	}
	var out []InboxItem
	for _, c := range cs {
		themID := c.ListerID
		read := c.RenterReadAt
		if c.ListerID == user {
			themID, read = c.RenterID, c.ListerReadAt
		}
		it := InboxItem{C: c}
		if it.Them, err = s.db.User.Get(ctx, themID); err != nil {
			continue
		}
		if it.Listing, err = s.db.Listing.Get(ctx, c.ListingID); err != nil {
			continue
		}
		it.Last, _ = s.db.Message.Query().Where(message.ConversationID(c.ID)).Order(ent.Desc(message.FieldID)).First(ctx)
		it.Unread = c.LastSenderID != nil && *c.LastSenderID != user && (read == nil || read.Before(*c.LastMessageAt))
		out = append(out, it)
	}
	return out, nil
}

// ── Reports ──────────────────────────────────────────────────────────────

// ReportMessage files a report about a message from the other side.
func (s *Service) ReportMessage(ctx context.Context, a Actor, t *Thread, msgID uuid.UUID, reason, note string) error {
	m, err := s.db.Message.Query().Where(message.ID(msgID), message.ConversationID(t.C.ID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("messages: report: %w", err)
	}
	if m.SenderID == a.UserID {
		return ValidationError{"form": "You can't report your own message."}
	}
	valid := false
	for _, r := range ReportReasons {
		valid = valid || r.Key == reason
	}
	if !valid {
		reason = "other"
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > ReportNoteLimit {
		note = string([]rune(note)[:ReportNoteLimit])
	}
	dup, err := s.db.Report.Query().Where(report.ReporterID(a.UserID), report.TargetTypeEQ(report.TargetTypeMessage), report.TargetID(msgID)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("messages: report dup: %w", err)
	}
	if dup {
		return nil // already reported; nothing more to do
	}
	r, err := s.db.Report.Create().SetReporterID(a.UserID).SetTargetType(report.TargetTypeMessage).SetTargetID(msgID).
		SetSubjectID(m.SenderID).SetReason(reason).SetNote(note).Save(ctx)
	if err != nil {
		return fmt.Errorf("messages: report: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: "message.reported", TargetType: "report", TargetID: r.ID.String(),
		IP: a.IP, UserAgent: a.UserAgent, Meta: map[string]any{"message": msgID.String(), "reason": reason}})
	return nil
}

// OpenReports is the moderators' queue, oldest first.
func (s *Service) OpenReports(ctx context.Context) ([]*ent.Report, error) {
	return s.db.Report.Query().Where(report.StatusEQ(report.StatusOpen)).Order(ent.Asc(report.FieldCreatedAt)).Limit(200).All(ctx)
}

// ReportContext is a reported message with the conversation around it.
type ReportContext struct {
	R        *ent.Report
	Message  *ent.Message
	Thread   []*ent.Message
	Conv     *ent.Conversation
	Reporter *ent.User
	Subject  *ent.User
	Listing  *ent.Listing
}

// ForReview loads a message report with context for moderators.
func (s *Service) ForReview(ctx context.Context, id uuid.UUID) (*ReportContext, error) {
	r, err := s.db.Report.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("messages: review: %w", err)
	}
	rc := &ReportContext{R: r}
	rc.Reporter, _ = s.db.User.Get(ctx, r.ReporterID)
	if r.SubjectID != nil {
		rc.Subject, _ = s.db.User.Get(ctx, *r.SubjectID)
	}
	if r.TargetType == report.TargetTypeListing {
		rc.Listing, _ = s.db.Listing.Get(ctx, r.TargetID)
		return rc, nil
	}
	if r.TargetType != report.TargetTypeMessage {
		return nil, ErrNotFound
	}
	if rc.Message, err = s.db.Message.Get(ctx, r.TargetID); err != nil {
		return nil, fmt.Errorf("messages: review message: %w", err)
	}
	if rc.Conv, err = s.db.Conversation.Get(ctx, rc.Message.ConversationID); err != nil {
		return nil, fmt.Errorf("messages: review conv: %w", err)
	}
	rc.Thread, _ = s.db.Message.Query().Where(message.ConversationID(rc.Conv.ID)).Order(ent.Asc(message.FieldID)).Limit(100).All(ctx)
	rc.Listing, _ = s.db.Listing.Get(ctx, rc.Conv.ListingID)
	return rc, nil
}

// Resolve closes a report as actioned or dismissed.
func (s *Service) Resolve(ctx context.Context, moderator Actor, id uuid.UUID, actioned bool) error {
	st := report.StatusDismissed
	if actioned {
		st = report.StatusActioned
	}
	n, err := s.db.Report.Update().Where(report.ID(id), report.StatusEQ(report.StatusOpen)).
		SetStatus(st).SetHandledBy(moderator.UserID).SetHandledAt(s.now()).Save(ctx)
	if err != nil {
		return fmt.Errorf("messages: resolve: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	s.audit.Record(ctx, audit.Event{Actor: &moderator.UserID, Action: "report." + string(st), TargetType: "report", TargetID: id.String(),
		IP: moderator.IP, UserAgent: moderator.UserAgent})
	return nil
}

// ListingReportReasons are the choices when reporting a listing.
var ListingReportReasons = []struct{ Key, Label string }{
	{"scam", "Asks for money before a viewing, or looks like a scam"},
	{"fake", "Photos or details aren't real"},
	{"wrong_price", "The price or fees are wrong"},
	{"not_theirs", "The lister doesn't own or manage it"},
	{"discrimination", "Discriminates against renters"},
	{"other", "Something else"},
}

// ReportListing files a report about a listing (not the reporter's own).
func (s *Service) ReportListing(ctx context.Context, a Actor, listingID uuid.UUID, reason, note string) error {
	l, err := s.db.Listing.Get(ctx, listingID)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("messages: report listing: %w", err)
	}
	if l.ListerID == a.UserID {
		return ErrForbidden
	}
	valid := false
	for _, r := range ListingReportReasons {
		valid = valid || r.Key == reason
	}
	if !valid {
		return ValidationError{"reason": "Choose what's wrong."}
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > ReportNoteLimit {
		note = string([]rune(note)[:ReportNoteLimit])
	}
	dup, err := s.db.Report.Query().Where(report.ReporterID(a.UserID), report.TargetTypeEQ(report.TargetTypeListing),
		report.TargetID(listingID), report.StatusEQ(report.StatusOpen)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("messages: report listing dup: %w", err)
	}
	if dup {
		return nil
	}
	r, err := s.db.Report.Create().SetReporterID(a.UserID).SetTargetType(report.TargetTypeListing).SetTargetID(listingID).
		SetSubjectID(l.ListerID).SetReason(reason).SetNote(note).Save(ctx)
	if err != nil {
		return fmt.Errorf("messages: report listing: %w", err)
	}
	s.audit.Record(ctx, audit.Event{Actor: &a.UserID, Action: "listing.reported", TargetType: "report", TargetID: r.ID.String(),
		IP: a.IP, UserAgent: a.UserAgent, Meta: map[string]any{"listing": listingID.String(), "reason": reason}})
	return nil
}
