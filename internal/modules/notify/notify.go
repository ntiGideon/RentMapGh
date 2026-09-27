// Package notify is the one way the app tells a user something
// (ProjectRequirement §6.12): every note lands in the in-app notification
// centre (the bell), pings the user's open pages, and — if their
// preferences allow that topic by SMS and it isn't the middle of the
// night — goes out as a text too.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/notification"
	"rentmapgh/internal/modules/messages"
	"rentmapgh/internal/modules/users"
	"rentmapgh/internal/platform/sms"
)

// Quiet hours for non-urgent texts (Ghana is GMT all year).
const (
	QuietFrom  = 21
	QuietUntil = 7
)

// Note is one notification.
type Note struct {
	Topic  string // preference topic (users.Topics): viewings, messages, listings…
	Kind   string // e.g. "viewing.confirmed"
	Title  string // one line for the bell
	Body   string // optional second line
	URL    string // where tapping it goes (a path)
	SMS    string // the text message; "" means in-app only
	Urgent bool   // send the SMS even in quiet hours (e.g. "your viewing is in 2 hours")
}

type Service struct {
	db  *ent.Client
	sms sms.Sender
	hub *messages.Hub
	now func() time.Time
}

func NewService(db *ent.Client, sender sms.Sender, hub *messages.Hub) *Service {
	return &Service{db: db, sms: sender, hub: hub, now: func() time.Time { return time.Now().UTC() }}
}

// Send delivers a note to a user. Failures are logged, not returned: a
// notification hiccup must never undo the action that caused it.
func (s *Service) Send(ctx context.Context, userID uuid.UUID, n Note) {
	u, err := s.db.User.Get(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "notify: user", "err", err)
		return
	}
	s.SendTo(ctx, u, n)
}

// SendTo is Send for a user already loaded.
func (s *Service) SendTo(ctx context.Context, u *ent.User, n Note) {
	err := s.db.Notification.Create().SetUserID(u.ID).SetTopic(n.Topic).SetKind(n.Kind).
		SetTitle(clip(n.Title, 140)).SetBody(clip(n.Body, 300)).SetURL(clip(n.URL, 300)).Exec(ctx)
	if err != nil {
		slog.WarnContext(ctx, "notify: store", "err", err)
	}
	if s.hub != nil {
		c, _ := s.Unread(ctx, u.ID)
		s.hub.Publish(u.ID, messages.Event{Kind: "notification", Unread: c})
	}
	if n.SMS == "" || u.Phone == nil || !users.Prefs(u)[n.Topic+".sms"] {
		return
	}
	if !n.Urgent && quiet(s.now()) {
		return
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
	defer cancel()
	if err := s.sms.Send(sendCtx, sms.Message{To: *u.Phone, Body: n.SMS}); err != nil {
		slog.WarnContext(ctx, "notify: sms", "kind", n.Kind, "err", err)
	}
}

func quiet(now time.Time) bool { h := now.Hour(); return h >= QuietFrom || h < QuietUntil }

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// Unread counts the user's unread notifications (the bell badge).
func (s *Service) Unread(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := s.db.Notification.Query().Where(notification.UserID(userID), notification.ReadAtIsNil()).Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("notify: unread: %w", err)
	}
	return n, nil
}

// List returns the user's latest notifications.
func (s *Service) List(ctx context.Context, userID uuid.UUID, limit int) ([]*ent.Notification, error) {
	ns, err := s.db.Notification.Query().Where(notification.UserID(userID)).
		Order(ent.Desc(notification.FieldCreatedAt)).Limit(limit).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("notify: list: %w", err)
	}
	return ns, nil
}

// MarkAllRead clears the bell.
func (s *Service) MarkAllRead(ctx context.Context, userID uuid.UUID) error {
	if err := s.db.Notification.Update().Where(notification.UserID(userID), notification.ReadAtIsNil()).
		SetReadAt(s.now()).Exec(ctx); err != nil {
		return fmt.Errorf("notify: read: %w", err)
	}
	if s.hub != nil {
		s.hub.Publish(userID, messages.Event{Kind: "notification", Unread: 0})
	}
	return nil
}
