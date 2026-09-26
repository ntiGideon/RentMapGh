package sms

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAfricasTalkingSend(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "key-123", r.Header.Get("apiKey"))
		b, _ := io.ReadAll(r.Body)
		got, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"SMSMessageData":{"Message":"Sent to 1/1 Total Cost: GHS 0.0300","Recipients":[{"statusCode":101,"number":"+233241234567","status":"Success","cost":"GHS 0.0300","messageId":"ATXid_1"}]}}`))
	}))
	defer srv.Close()

	p := NewAfricasTalking("rentmap", "key-123", "RentMap", false)
	p.Endpoint = srv.URL
	require.NoError(t, p.Send(context.Background(), Message{To: "+233241234567", Body: "hi"}))
	assert.Equal(t, "rentmap", got.Get("username"))
	assert.Equal(t, "+233241234567", got.Get("to"))
	assert.Equal(t, "hi", got.Get("message"))
	assert.Equal(t, "RentMap", got.Get("from"))
}

func TestAfricasTalkingRejections(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"blacklisted":   {201, `{"SMSMessageData":{"Message":"Sent to 0/1","Recipients":[{"statusCode":406,"number":"+233241234567","status":"UserInBlacklist"}]}}`},
		"no recipients": {201, `{"SMSMessageData":{"Message":"InvalidPhoneNumber","Recipients":[]}}`},
		"bad key":       {401, `The supplied authentication is invalid`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			p := NewAfricasTalking("sandbox", "k", "", true)
			p.Endpoint = srv.URL
			assert.ErrorIs(t, p.Send(context.Background(), Message{To: "+233241234567", Body: "x"}), ErrRejected)
		})
	}
}

func TestAfricasTalkingServerErrorIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	p := NewAfricasTalking("sandbox", "k", "", true)
	p.Endpoint = srv.URL
	err := p.Send(context.Background(), Message{To: "+233241234567", Body: "x"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRejected)
}

func TestConfigValidate(t *testing.T) {
	assert.NoError(t, Config{Provider: "mailpit", SenderID: "RentMap"}.Validate(false))
	assert.Error(t, Config{Provider: "mailpit"}.Validate(true), "dev providers refused in production")
	assert.Error(t, Config{Provider: "africastalking"}.Validate(false), "needs credentials")
	assert.Error(t, Config{Provider: "africastalking", ATUsername: "u", ATAPIKey: "k", ATSandbox: true}.Validate(true))
	assert.NoError(t, Config{Provider: "africastalking", ATUsername: "u", ATAPIKey: "k"}.Validate(true))
	assert.Error(t, Config{Provider: "carrier-pigeon"}.Validate(false))
}
