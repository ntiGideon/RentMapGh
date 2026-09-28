// Package errreport sends errors to Sentry when SENTRY_DSN is set: every
// ERROR log line (which includes recovered panics, logged with their
// stack) becomes an event. Without a DSN it does nothing.
//
// Privacy: events carry the log message and its attributes only — no
// request bodies, cookies or headers, and send_default_pii is off. Log
// attributes never include phone numbers or message text.
package errreport

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/getsentry/sentry-go"
)

// Init starts the Sentry client. flush waits for queued events on shutdown.
func Init(dsn, env, release string) (flush func(), err error) {
	if dsn == "" {
		return func() {}, nil
	}
	err = sentry.Init(sentry.ClientOptions{
		Dsn: dsn, Environment: env, Release: release,
		SendDefaultPII: false, AttachStacktrace: true, SampleRate: 1,
	})
	if err != nil {
		return func() {}, fmt.Errorf("errreport: %w", err)
	}
	return func() { sentry.Flush(3 * time.Second) }, nil
}

// Handler wraps a slog.Handler: records at ERROR and above also go to
// Sentry (when Init ran with a DSN).
func Handler(next slog.Handler) slog.Handler { return &handler{next: next} }

type handler struct {
	next  slog.Handler
	attrs []slog.Attr
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError && sentry.CurrentHub().Client() != nil {
		capture(r, h.attrs)
	}
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	return &handler{next: h.next.WithAttrs(as), attrs: append(append([]slog.Attr{}, h.attrs...), as...)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{next: h.next.WithGroup(name), attrs: h.attrs}
}

func capture(r slog.Record, base []slog.Attr) {
	extra := map[string]any{}
	var errText, stack string
	add := func(a slog.Attr) bool {
		switch a.Key {
		case "err":
			errText = a.Value.String()
		case "stack":
			stack = a.Value.String()
		default:
			extra[a.Key] = a.Value.String()
		}
		return true
	}
	for _, a := range base {
		add(a)
	}
	r.Attrs(add)
	sentry.WithScope(func(s *sentry.Scope) {
		s.SetLevel(sentry.LevelError)
		if stack != "" {
			extra["stack"] = stack
		}
		s.SetContext("log", sentry.Context(extra))
		if id, ok := extra["req_id"]; ok {
			s.SetTag("req_id", fmt.Sprint(id))
		}
		// Group by the log message ("panic", "admin: flagged", …), not the
		// error text, which often carries IDs.
		s.SetFingerprint([]string{r.Message})
		msg := r.Message
		if errText != "" {
			msg += ": " + errText
		}
		sentry.CaptureMessage(msg)
	})
}
