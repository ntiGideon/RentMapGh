// Package sms sends text messages through a pluggable provider.
//
//	log            prints messages to the server log (tests, CI)
//	mailpit        delivers each SMS as an email to Mailpit (local dev: http://localhost:8025)
//	africastalking Africa's Talking — sandbox (free, with a phone simulator) or live
//
// Adding Arkesel, Hubtel or mNotify means one more Sender implementation.
package sms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// Message is a single SMS to one E.164 number.
type Message struct {
	To   string
	Body string
}

// Sender delivers a message. Implementations must be safe for concurrent use
// and must respect ctx cancellation.
type Sender interface {
	Send(ctx context.Context, m Message) error
	Name() string
}

// ErrRejected means the provider refused the message (bad number, blocked,
// out of credit). Retrying the same message won't help.
var ErrRejected = errors.New("sms: rejected by provider")

type Config struct {
	Provider string `env:"SMS_PROVIDER" envDefault:"log"`
	SenderID string `env:"SMS_SENDER_ID" envDefault:"RentMap"` // alphanumeric sender, ≤11 chars, must be registered with the provider

	MailpitSMTPAddr string `env:"MAILPIT_SMTP_ADDR" envDefault:"localhost:1025"`
	MailpitWebURL   string `env:"MAILPIT_WEB_URL" envDefault:"http://localhost:8025"`

	ATUsername string `env:"AT_USERNAME"`
	ATAPIKey   string `env:"AT_API_KEY"`
	ATSandbox  bool   `env:"AT_SANDBOX" envDefault:"true"`
}

func (c Config) Validate(production bool) error {
	switch c.Provider {
	case "log", "mailpit":
		if production {
			return fmt.Errorf("SMS_PROVIDER=%s is for development only", c.Provider)
		}
	case "africastalking":
		if c.ATUsername == "" || c.ATAPIKey == "" {
			return errors.New("SMS_PROVIDER=africastalking needs AT_USERNAME and AT_API_KEY")
		}
		if production && c.ATSandbox {
			return errors.New("AT_SANDBOX must be false in production")
		}
	default:
		return fmt.Errorf("unknown SMS_PROVIDER %q (log, mailpit, africastalking)", c.Provider)
	}
	if len(c.SenderID) > 11 {
		return errors.New("SMS_SENDER_ID must be at most 11 characters")
	}
	return nil
}

// New builds the Sender selected by cfg. Call cfg.Validate first.
func New(cfg Config) Sender {
	switch cfg.Provider {
	case "mailpit":
		return &Mailpit{Addr: cfg.MailpitSMTPAddr, SenderID: cfg.SenderID}
	case "africastalking":
		return NewAfricasTalking(cfg.ATUsername, cfg.ATAPIKey, cfg.SenderID, cfg.ATSandbox)
	default:
		return Log{}
	}
}

// Log writes messages to slog. Codes appear in the log, so never use it in
// production (Config.Validate refuses it).
type Log struct{}

func (Log) Name() string { return "log" }

func (Log) Send(ctx context.Context, m Message) error {
	slog.InfoContext(ctx, "sms (log provider)", "to", m.To, "body", m.Body)
	return nil
}

// Capture records messages in memory, for tests.
type Capture struct {
	mu   sync.Mutex
	msgs []Message
	Err  error // returned by Send when set
}

func (*Capture) Name() string { return "capture" }

func (c *Capture) Send(_ context.Context, m Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	c.msgs = append(c.msgs, m)
	return nil
}

func (c *Capture) Messages() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.msgs...)
}

// Last returns the most recent message, or the zero Message.
func (c *Capture) Last() Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.msgs) == 0 {
		return Message{}
	}
	return c.msgs[len(c.msgs)-1]
}
