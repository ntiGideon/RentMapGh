package errreport

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorLogsBecomeEvents(t *testing.T) {
	var mu sync.Mutex
	var got []*sentry.Event
	require.NoError(t, sentry.Init(sentry.ClientOptions{
		Dsn: "https://public@example.invalid/1",
		BeforeSend: func(e *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
			return nil // never leaves the test
		},
	}))
	defer sentry.Flush(time.Second)

	var out bytes.Buffer
	log := slog.New(Handler(slog.NewTextHandler(&out, nil))).With("req_id", "abc")
	log.Info("fine")
	log.Warn("careful")
	log.Error("admin: flagged", "err", "boom", "stack", "goroutine 1")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 1, "only ERROR and above")
	assert.Equal(t, "admin: flagged: boom", got[0].Message)
	assert.Equal(t, []string{"admin: flagged"}, got[0].Fingerprint)
	assert.Equal(t, "abc", got[0].Tags["req_id"])
	assert.Equal(t, "goroutine 1", got[0].Contexts["log"]["stack"])
	assert.Contains(t, out.String(), "careful", "logs still reach the real handler")
}

func TestNoDSNIsANoop(t *testing.T) {
	flush, err := Init("", "test", "")
	require.NoError(t, err)
	flush()
}
