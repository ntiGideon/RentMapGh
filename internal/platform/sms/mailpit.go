package sms

import (
	"context"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Mailpit delivers each SMS as a plain-text email to the Mailpit container,
// so the dev inbox (http://localhost:8025) doubles as a phone. The recipient
// address is the phone number, e.g. <+233241234567@sms.rentmap.local>.
type Mailpit struct {
	Addr     string // SMTP host:port, e.g. localhost:1025
	SenderID string
}

func (*Mailpit) Name() string { return "mailpit" }

func (p *Mailpit) Send(ctx context.Context, m Message) error {
	to := strings.TrimPrefix(m.To, "+") + "@sms.rentmap.local"
	from := "sms@rentmap.local"
	msg := strings.Join([]string{
		"From: " + p.SenderID + " SMS <" + from + ">",
		"To: " + m.To + " <" + to + ">",
		"Subject: SMS to " + m.To,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"X-RentMap-SMS: 1",
		"",
		m.Body,
	}, "\r\n")

	// net/smtp has no context support: bound the whole exchange with a deadline.
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return fmt.Errorf("sms mailpit: dial %s (is `task db:up` running?): %w", p.Addr, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	host, _, _ := net.SplitHostPort(p.Addr)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("sms mailpit: %w", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("sms mailpit: MAIL: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("sms mailpit: RCPT: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("sms mailpit: DATA: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("sms mailpit: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("sms mailpit: %w", err)
	}
	return c.Quit()
}
