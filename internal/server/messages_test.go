package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	convRe = regexp.MustCompile(`^/messages/([0-9a-f-]{36})$`)
	msgRe  = regexp.MustCompile(`<li data-id="([0-9a-f-]{36})"`)
)

func TestMessagingOverHTTP(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ll, ids := liveListingsBy(t, h, d, capture, "0244000071", 1)
	id := ids[0]

	// The gate, then the first-message page.
	anon := newBrowser(t, h)
	rec := anon.do("GET", "/l/"+id+"/message", nil, false)
	assert.Contains(t, rec.Header().Get("Location"), "/login?next=")
	renter := signInAs(t, h, d, capture, "0244000072", "Kofi Mensah", "renter")
	rec = renter.do("GET", "/l/"+id+"/message", nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Message Akua Owusu")

	rec = renter.do("POST", "/l/"+id+"/message", url.Values{"body": {"Hi! Is it still available? Call me on 024 555 1234"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	m := convRe.FindStringSubmatch(rec.Header().Get("Location"))
	require.Len(t, m, 2)
	conv := "/messages/" + m[1]
	assert.Equal(t, "+233244000071", capture.Last().To)
	assert.Contains(t, capture.Last().Body, "Kofi sent you a message")

	// The landlord's badge and inbox show it unread; the number is hidden.
	page := ll.do("GET", "/listings", nil, false).Body.String()
	assert.Regexp(t, `data-badge="unread" class="[^"]*grid[^"]*">1<`, page)
	inbox := ll.do("GET", "/messages", nil, false).Body.String()
	assert.Contains(t, inbox, "Kofi Mensah")
	assert.Contains(t, inbox, `aria-label="Unread"`)
	assert.NotContains(t, inbox, "555 1234")
	thread := ll.do("GET", conv, nil, false).Body.String()
	assert.Contains(t, thread, "[number hidden until a viewing is confirmed]")
	assert.NotContains(t, thread, "555 1234")
	assert.NotContains(t, ll.do("GET", "/messages", nil, false).Body.String(), `aria-label="Unread"`, "read now")

	// The renter sees their own number, and "Seen".
	thread = renter.do("GET", conv, nil, false).Body.String()
	assert.Contains(t, thread, "024 555 1234")
	assert.Contains(t, thread, "· Seen")

	// A scammy reply over htmx: the bubbles come back; the renter gets a warning.
	last := msgRe.FindAllStringSubmatch(thread, -1)
	rec = ll.do("POST", conv, url.Values{"body": {"Yes. Send the commitment fee on MoMo today only"}, "after": {last[len(last)-1][1]}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, strings.Count(rec.Body.String(), "<li data-id="), "only the new message")
	thread = renter.do("GET", conv, nil, false).Body.String()
	assert.Contains(t, thread, "Never pay rent or fees")
	assert.Contains(t, thread, "RentMap never charges booking or commitment fees")
	assert.Contains(t, thread, ">Report</summary>")

	// Empty messages are refused as plain text for the script.
	rec = renter.do("POST", conv, url.Values{"body": {"  "}}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// The renter reports it; a moderator sees it in the queue.
	all := msgRe.FindAllStringSubmatch(thread, -1)
	scam := all[len(all)-1][1]
	rec = renter.do("POST", conv+"/report/"+scam, url.Values{"reason": {"scam"}, "note": {"Wants money first"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Reported")
	mod := signInAs(t, h, d, capture, "0244000079", "Esi", "renter", "moderator")
	queue := mod.do("GET", "/admin/reports", nil, false).Body.String()
	assert.Contains(t, queue, "Asking for money or looks like a scam")
	rid := regexp.MustCompile(`/admin/reports/([0-9a-f-]{36})`).FindStringSubmatch(queue)
	require.Len(t, rid, 2)
	review := mod.do("GET", "/admin/reports/"+rid[1], nil, false).Body.String()
	assert.Contains(t, review, "Send the commitment fee")
	assert.Contains(t, review, "Wants money first")
	rec = mod.do("POST", "/admin/reports/"+rid[1]+"/decision", url.Values{"decision": {"actioned"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, mod.do("GET", "/admin/reports", nil, false).Body.String(), "No open reports")

	// Strangers can't read it; listers can't message their own place.
	stranger := signInAs(t, h, d, capture, "0244000073", "Yaw", "renter")
	assert.Equal(t, http.StatusNotFound, stranger.do("GET", conv, nil, false).Code)
	assert.Equal(t, http.StatusForbidden, ll.do("GET", "/l/"+id+"/message", nil, false).Code)
}

func TestEventStream(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ll, ids := liveListingsBy(t, h, d, capture, "0244000081", 1)
	renter := signInAs(t, h, d, capture, "0244000082", "Kofi", "renter")
	srv := httptest.NewServer(h)
	defer srv.Close()

	// The landlord opens a stream.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	for _, c := range ll.cookies {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	assert.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))
	lines := bufio.NewScanner(res.Body)
	next := func(prefix string) string {
		for lines.Scan() {
			if strings.HasPrefix(lines.Text(), prefix) {
				return lines.Text()
			}
		}
		return ""
	}
	assert.Equal(t, "event: unread", next("event:"), "starts with the count")
	assert.Contains(t, next("data:"), `"unread":0`)

	// A renter writes; the landlord's stream hears it with the new count.
	rec := renter.do("POST", "/l/"+ids[0]+"/message", url.Values{"body": {"Hello"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "event: message", next("event:"))
	assert.Contains(t, next("data:"), `"unread":1`)
}
