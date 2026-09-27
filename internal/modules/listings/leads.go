package listings

import (
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/agentmandate"
	"rentmapgh/internal/ent/conversation"
	"rentmapgh/internal/ent/viewing"
	"rentmapgh/internal/modules/mandates"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

// Lead stages, in pipeline order.
var leadStages = []struct{ Key, Label string }{
	{"messaged", "Messaged"},
	{"requested", "Viewing requested"},
	{"booked", "Viewing booked"},
	{"viewed", "Viewed"},
	{"missed", "Didn't come"},
	{"closed", "Closed"},
}

type lead struct {
	renter, listing uuid.UUID
	stage           string
	at              time.Time
	url             string
}

// Leads is /listings/leads: everyone who got in touch, by stage. Agents
// also see their mandates and the landlords behind them.
func (h *Handler) Leads(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	ctx := r.Context()
	now := h.svc.now()
	byKey := map[[2]uuid.UUID]*lead{}
	rank := func(s string) int {
		return slices.IndexFunc(leadStages, func(x struct{ Key, Label string }) bool { return x.Key == s })
	}
	keep := func(l lead) {
		k := [2]uuid.UUID{l.renter, l.listing}
		cur, ok := byKey[k]
		if !ok || rank(l.stage) > rank(cur.stage) || (l.stage == cur.stage && l.at.After(cur.at)) {
			if ok && l.at.Before(cur.at) {
				l.at = cur.at
			}
			byKey[k] = &l
		}
	}
	convs, err := h.svc.db.Conversation.Query().Where(conversation.ListerID(a.UserID), conversation.LastMessageAtNotNil()).All(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "leads: conversations", "err", err)
	}
	for _, c := range convs {
		keep(lead{renter: c.RenterID, listing: c.ListingID, stage: "messaged", at: *c.LastMessageAt, url: "/messages/" + c.ID.String()})
	}
	vs, err := h.svc.db.Viewing.Query().Where(viewing.ListerID(a.UserID)).All(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "leads: viewings", "err", err)
	}
	for _, v := range vs {
		st := "closed"
		switch {
		case v.Status == viewing.StatusRequested || v.Status == viewing.StatusProposed:
			st = "requested"
		case v.Status == viewing.StatusConfirmed && v.StartsAt.After(now):
			st = "booked"
		case v.Status == viewing.StatusConfirmed || v.Status == viewing.StatusCompleted:
			st = "viewed"
		case v.Status == viewing.StatusNoShow:
			st = "missed"
		}
		keep(lead{renter: v.RenterID, listing: v.ListingID, stage: st, at: v.UpdatedAt, url: "/viewings/" + v.ID.String()})
	}

	var all []*lead
	for _, l := range byKey {
		all = append(all, l)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	names := map[uuid.UUID]string{}
	titles := map[uuid.UUID]string{}
	v := pages.LeadsView{IsAgent: slices.Contains(a.Roles, "agent")}
	for _, s := range leadStages {
		v.Stages = append(v.Stages, pages.LeadStage{Key: s.Key, Label: s.Label})
	}
	for _, l := range all {
		if _, ok := names[l.renter]; !ok {
			if u, err := h.svc.db.User.Get(ctx, l.renter); err == nil {
				names[l.renter] = orDash(u.Name)
			}
		}
		if _, ok := titles[l.listing]; !ok {
			if li, err := h.svc.db.Listing.Get(ctx, l.listing); err == nil {
				titles[l.listing] = orDash(li.Headline)
			}
		}
		i := rank(l.stage)
		v.Stages[i].Rows = append(v.Stages[i].Rows, pages.LeadRow{Name: names[l.renter], Listing: titles[l.listing], URL: l.url, When: ago(l.at, now)})
	}
	if v.IsAgent {
		h.agentSection(r, a.UserID, &v)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	render.Component(w, r, http.StatusOK, pages.Leads(layouts.Meta{Title: "Leads", NoIndex: true}, v))
}

// agentSection fills an agent's mandates and clients.
func (h *Handler) agentSection(r *http.Request, agent uuid.UUID, v *pages.LeadsView) {
	ctx := r.Context()
	ms, err := h.svc.db.AgentMandate.Query().Where(agentmandate.AgentID(agent)).Order(ent.Desc(agentmandate.FieldCreatedAt)).All(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "leads: mandates", "err", err)
		return
	}
	now := h.svc.now()
	latest := map[uuid.UUID]bool{} // one row per property: the newest mandate
	clients := map[string]*pages.Client{}
	for _, m := range ms {
		if latest[m.PropertyID] {
			continue
		}
		latest[m.PropertyID] = true
		p, err := h.svc.db.Property.Get(ctx, m.PropertyID)
		if err != nil {
			continue
		}
		st := mandates.StateOf(m, now)
		row := pages.MandateRow{Property: orDash(p.Name), Landlord: orDash(m.LandlordName), Phone: phone.Mask(m.LandlordPhone), State: string(st)}
		if p.Name == "" {
			row.Property = "Property in " + area(&Item{P: p})
		}
		if m.ValidUntil != nil && st == mandates.Approved {
			row.Until = m.ValidUntil.Format("2 Jan 2006")
		}
		v.Mandates = append(v.Mandates, row)
		if st == mandates.Approved {
			c, ok := clients[m.LandlordPhone]
			if !ok {
				c = &pages.Client{Name: orDash(m.LandlordName), Phone: phone.Mask(m.LandlordPhone)}
				clients[m.LandlordPhone] = c
			}
			c.Properties++
		}
	}
	for _, c := range clients {
		v.Clients = append(v.Clients, *c)
	}
	sort.Slice(v.Clients, func(i, j int) bool { return v.Clients[i].Properties > v.Clients[j].Properties })
}
