package sms

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	atLiveURL    = "https://api.africastalking.com/version1/messaging"
	atSandboxURL = "https://api.sandbox.africastalking.com/version1/messaging"
)

// AfricasTalking sends through Africa's Talking's messaging API.
//
// Sandbox: create a free account at https://account.africastalking.com, open
// the sandbox app, generate an API key and use username "sandbox". Messages
// show up in the sandbox phone simulator (https://simulator.africastalking.com)
// once you "launch" it with the same number — nothing is actually delivered.
type AfricasTalking struct {
	Username string
	APIKey   string
	SenderID string // empty → the provider's default shortcode
	Endpoint string
	Client   *http.Client
}

func NewAfricasTalking(username, apiKey, senderID string, sandbox bool) *AfricasTalking {
	endpoint := atLiveURL
	if sandbox {
		endpoint = atSandboxURL
	}
	return &AfricasTalking{
		Username: username, APIKey: apiKey, SenderID: senderID, Endpoint: endpoint,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (*AfricasTalking) Name() string { return "africastalking" }

type atResponse struct {
	SMSMessageData struct {
		Message    string `json:"Message"`
		Recipients []struct {
			StatusCode int    `json:"statusCode"`
			Number     string `json:"number"`
			Status     string `json:"status"`
			Cost       string `json:"cost"`
			MessageID  string `json:"messageId"`
		} `json:"Recipients"`
	} `json:"SMSMessageData"`
}

func (p *AfricasTalking) Send(ctx context.Context, m Message) error {
	form := url.Values{"username": {p.Username}, "to": {m.To}, "message": {m.Body}}
	// The sandbox rejects unregistered alphanumeric sender IDs, so only send
	// one against the live API.
	if p.SenderID != "" && p.Endpoint != atSandboxURL {
		form.Set("from", p.SenderID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("sms africastalking: %w", err)
	}
	req.Header.Set("apiKey", p.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("sms africastalking: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))

	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("sms africastalking: bad credentials (check AT_USERNAME / AT_API_KEY): %w", ErrRejected)
	case res.StatusCode >= 500:
		return fmt.Errorf("sms africastalking: provider error %d", res.StatusCode)
	case res.StatusCode >= 300:
		return fmt.Errorf("sms africastalking: status %d: %s: %w", res.StatusCode, truncate(string(body), 200), ErrRejected)
	}

	var out atResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("sms africastalking: decode response: %w", err)
	}
	if len(out.SMSMessageData.Recipients) == 0 {
		return fmt.Errorf("sms africastalking: %s: %w", out.SMSMessageData.Message, ErrRejected)
	}
	r := out.SMSMessageData.Recipients[0]
	switch r.StatusCode {
	case 100, 101, 102: // Processed, Sent, Queued
		return nil
	default:
		return fmt.Errorf("sms africastalking: %s (code %d): %w", r.Status, r.StatusCode, ErrRejected)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
