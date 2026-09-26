package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	SessionCookie = "rm_session"
	// pendingCookie remembers which phone is mid-verification, so the number
	// never travels in a URL (logs, history, Referer).
	pendingCookie = "rm_otp"
	pendingTTL    = 15 * time.Minute
)

type cookies struct {
	secret []byte
	secure bool
}

func (c cookies) setSession(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is on whenever the site is served over HTTPS
		Name: SessionCookie, Value: token, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (c cookies) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Path: "/", MaxAge: -1, //nolint:gosec // G124: see setSession
		HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode})
}

func (c cookies) setPending(w http.ResponseWriter, e164 string, now time.Time) {
	exp := strconv.FormatInt(now.Add(pendingTTL).Unix(), 10)
	payload := e164 + "|" + exp
	v := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + c.sign(payload)
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: see setSession
		Name: pendingCookie, Value: v, Path: "/login", MaxAge: int(pendingTTL.Seconds()),
		HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode,
	})
}

// pending returns the phone being verified, or "" if the cookie is missing,
// tampered with or expired.
func (c cookies) pending(r *http.Request, now time.Time) string {
	ck, err := r.Cookie(pendingCookie)
	if err != nil {
		return ""
	}
	enc, sig, ok := strings.Cut(ck.Value, ".")
	if !ok {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return ""
	}
	payload := string(raw)
	if !hmac.Equal([]byte(sig), []byte(c.sign(payload))) {
		return ""
	}
	e164, exp, ok := strings.Cut(payload, "|")
	unix, err := strconv.ParseInt(exp, 10, 64)
	if !ok || err != nil || now.Unix() > unix {
		return ""
	}
	return e164
}

func (c cookies) clearPending(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Path: "/login", MaxAge: -1, //nolint:gosec // G124: see setSession
		HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode})
}

func (c cookies) sign(payload string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte("pending:v1:" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// SafeNext returns next if it's a local path, otherwise "". It blocks open
// redirects such as "//evil.com" and "/\evil.com".
func SafeNext(next string) string {
	if next == "" || len(next) > 512 || next[0] != '/' {
		return ""
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return ""
	}
	if strings.ContainsAny(next, "\r\n") {
		return ""
	}
	return next
}
