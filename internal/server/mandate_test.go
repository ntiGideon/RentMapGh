package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rentmapgh/internal/ent/agentmandate"
)

var mandateLinkRe = regexp.MustCompile(`http://example\.test(/m/[A-Za-z0-9_-]{43})`)

// fillWizard completes every step up to review (photos skipped).
func fillWizard(t *testing.T, b *browser, id string) {
	t.Helper()
	for _, st := range []struct {
		step string
		form url.Values
	}{
		{"location", url.Values{"lat": {"6.66970"}, "lng": {"-1.55880"}, "landmark": {"Blue gate"}, "neighbourhood": {"ayigya"}}},
		{"property", url.Values{"category": {"hostel"}, "name": {"Adom Hostel"}}},
		{"unit", url.Values{"unit_type": {"hostel_2in1"}, "furnished": {"full"}, "self_contained": {"yes"}, "meter_type": {"prepaid_shared"},
			"water_source": {"borehole"}, "kitchen": {"shared"}, "bathrooms": {"1"}}},
		{"amenities", url.Values{"amenities": {"wifi"}}},
		{"photos", url.Values{"later": {"1"}}},
		{"pricing", url.Values{"rent": {"3,200"}, "rent_period": {"academic_year"}, "advance_periods": {"1"}, "deposit": {"0"}, "agent_fee": {"0"},
			"service_charge": {"0"}, "viewing_fee": {"0"}}},
		{"details", url.Values{"headline": {"2-in-a-room hostel near KNUST"}, "description": {"Borehole water."}}},
	} {
		rec := b.do("POST", "/listings/"+id+"/edit/"+st.step, st.form, false)
		require.Equal(t, http.StatusSeeOther, rec.Code, st.step+": "+rec.Body.String())
	}
}

func TestAgentMandateOverHTTP(t *testing.T) {
	h, d, capture := newTestServerSMS(t)
	ctx := context.Background()
	agent := signInAs(t, h, d, capture, "0244000001", "Kwame Mensah", "agent")
	rec := agent.do("POST", "/listings/new", url.Values{}, false)
	id := editRe.FindStringSubmatch(rec.Header().Get("Location"))[1]
	fillWizard(t, agent, id)
	review := "/listings/" + id + "/edit/review"

	// Before asking: the card with the form.
	rec = agent.do("GET", review, nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `id="owner-authority"`)
	assert.Contains(t, rec.Body.String(), "Owner not confirmed")

	// A bad number comes back on the form, typed values kept.
	rec = agent.do("POST", "/listings/"+id+"/owner", url.Values{"landlord_name": {"Mrs Owusu"}, "landlord_phone": {"12"}, "months": {"6"}}, false)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "Ghana number")
	assert.Contains(t, rec.Body.String(), `value="Mrs Owusu"`)

	rec = agent.do("POST", "/listings/"+id+"/owner", url.Values{"landlord_name": {"Mrs Owusu"}, "landlord_phone": {"020 111 2222"}, "months": {"6"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, review+"#owner-authority", rec.Header().Get("Location"))
	sms := capture.Last()
	assert.Equal(t, "+233201112222", sms.To)
	m := mandateLinkRe.FindStringSubmatch(sms.Body)
	require.Len(t, m, 2, sms.Body)
	link := m[1]

	rec = agent.do("GET", review, nil, false)
	assert.Contains(t, rec.Body.String(), "We texted Mrs Owusu")
	assert.Contains(t, rec.Body.String(), "Owner asked")
	rec = agent.do("GET", "/listings", nil, false)
	assert.Contains(t, rec.Body.String(), "Owner asked")

	// The landlord's page: no account, no cookies.
	landlord := newBrowser(t, h)
	rec = landlord.do("GET", link, nil, false)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "May this agent list your property?")
	assert.Contains(t, body, "Kwame Mensah")
	assert.Contains(t, body, "024 400 0001")
	assert.Contains(t, body, "Adom Hostel")
	assert.Contains(t, body, "6 months")
	assert.Contains(t, rec.Header().Get("Cache-Control"), "no-store")
	assert.Contains(t, body, `name="robots" content="noindex"`)
	assert.Equal(t, http.StatusNotFound, landlord.do("GET", "/m/nope", nil, false).Code)

	// Cross-site posts are refused (CSRF).
	rec = do(h, "POST", link, url.Values{"decision": {"approve"}}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = landlord.do("POST", link, url.Values{"decision": {"approve"}}, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Approved")
	assert.Contains(t, capture.Last().Body, "approved you to list Adom Hostel")
	assert.Equal(t, "+233244000001", capture.Last().To)

	// Answering twice: a clear message, not an error page.
	rec = landlord.do("POST", link, url.Values{"decision": {"decline"}}, false)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "already been answered")

	rec = agent.do("GET", review, nil, false)
	assert.Contains(t, rec.Body.String(), "approved you to list this property until")
	assert.Contains(t, rec.Body.String(), "Owner confirmed")

	// Withdraw from the same link.
	rec = landlord.do("GET", link, nil, false)
	assert.Contains(t, rec.Body.String(), "Withdraw approval")
	rec = landlord.do("POST", link, url.Values{"decision": {"withdraw"}}, false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Approval withdrawn")
	st := d.Ent.AgentMandate.Query().OnlyX(ctx).Status
	assert.Equal(t, agentmandate.StatusRevoked, st)

	// Owners don't get the card or the routes; renters get nothing.
	owner := signInAs(t, h, d, capture, "0244000002", "Ama", "landlord")
	rec = owner.do("POST", "/listings/new", url.Values{}, false)
	oid := editRe.FindStringSubmatch(rec.Header().Get("Location"))[1]
	fillWizard(t, owner, oid)
	rec = owner.do("GET", "/listings/"+oid+"/edit/review", nil, false)
	assert.NotContains(t, rec.Body.String(), `id="owner-authority"`)
	assert.Equal(t, http.StatusForbidden, owner.do("POST", "/listings/"+oid+"/owner", url.Values{"landlord_phone": {"0201112222"}}, false).Code)
	// Another agent can't ask on this agent's listing.
	other := signInAs(t, h, d, capture, "0244000003", "Yaw", "agent")
	assert.Equal(t, http.StatusNotFound, other.do("POST", "/listings/"+id+"/owner", url.Values{"landlord_phone": {"0201112222"}}, false).Code)
}
