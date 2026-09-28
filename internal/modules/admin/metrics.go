package admin

import (
	"context"
	"time"

	"rentmapgh/internal/ent/conversation"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/roleassignment"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/ent/viewing"
)

// Metrics is the platform at a glance. The north star is places rented
// through RentMap (listings.rented_via = rentmap, from "Did you find your
// tenant through RentMap?").
type Metrics struct {
	Users, Users7, Renters, Landlords, Agents int
	Live, Pending, Listed30                   int
	Viewings30, Held30, NoShows30             int
	Conversations30                           int
	Rented30, RentedVia30, RentedViaAll       int
	Weeks                                     []Week // oldest first
}

type Week struct {
	Start                                time.Time
	Users, Listings, Viewings, RentedVia int
}

// Metrics counts are best effort: a failed count shows as 0 rather than
// taking the page down.
func (s *Service) Metrics(ctx context.Context) Metrics {
	now := s.now()
	d7, d30 := now.AddDate(0, 0, -7), now.AddDate(0, 0, -30)
	role := func(r roleassignment.Role) int {
		n, _ := s.db.User.Query().Where(user.StatusEQ(user.StatusActive), user.HasRolesWith(roleassignment.RoleEQ(r))).Count(ctx)
		return n
	}
	var m Metrics
	m.Users, _ = s.db.User.Query().Where(user.StatusEQ(user.StatusActive)).Count(ctx)
	m.Users7, _ = s.db.User.Query().Where(user.CreatedAtGTE(d7)).Count(ctx)
	m.Renters, m.Landlords, m.Agents = role(roleassignment.RoleRenter), role(roleassignment.RoleLandlord), role(roleassignment.RoleAgent)
	m.Live, _ = s.db.Listing.Query().Where(listing.StatusEQ(listing.StatusActive)).Count(ctx)
	m.Pending, _ = s.db.Listing.Query().Where(listing.StatusEQ(listing.StatusPendingReview)).Count(ctx)
	m.Listed30, _ = s.db.Listing.Query().Where(listing.PublishedAtGTE(d30)).Count(ctx)
	m.Viewings30, _ = s.db.Viewing.Query().Where(viewing.CreatedAtGTE(d30)).Count(ctx)
	m.Held30, _ = s.db.Viewing.Query().Where(viewing.StartsAtGTE(d30), viewing.StatusEQ(viewing.StatusCompleted)).Count(ctx)
	m.NoShows30, _ = s.db.Viewing.Query().Where(viewing.StartsAtGTE(d30), viewing.StatusEQ(viewing.StatusNoShow)).Count(ctx)
	m.Conversations30, _ = s.db.Conversation.Query().Where(conversation.CreatedAtGTE(d30)).Count(ctx)
	m.Rented30, _ = s.db.Listing.Query().Where(listing.RentedAtGTE(d30)).Count(ctx)
	m.RentedVia30, _ = s.db.Listing.Query().Where(listing.RentedAtGTE(d30), listing.RentedViaEQ(listing.RentedViaRentmap)).Count(ctx)
	m.RentedViaAll, _ = s.db.Listing.Query().Where(listing.RentedViaEQ(listing.RentedViaRentmap)).Count(ctx)

	// Eight whole weeks, Monday to Monday (Ghana is on UTC).
	y, mo, d := now.Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
	monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	for i := 7; i >= 0; i-- {
		from := monday.AddDate(0, 0, -7*i)
		to := from.AddDate(0, 0, 7)
		w := Week{Start: from}
		w.Users, _ = s.db.User.Query().Where(user.CreatedAtGTE(from), user.CreatedAtLT(to)).Count(ctx)
		w.Listings, _ = s.db.Listing.Query().Where(listing.PublishedAtGTE(from), listing.PublishedAtLT(to)).Count(ctx)
		w.Viewings, _ = s.db.Viewing.Query().Where(viewing.CreatedAtGTE(from), viewing.CreatedAtLT(to)).Count(ctx)
		w.RentedVia, _ = s.db.Listing.Query().Where(listing.RentedAtGTE(from), listing.RentedAtLT(to),
			listing.RentedViaEQ(listing.RentedViaRentmap)).Count(ctx)
		m.Weeks = append(m.Weeks, w)
	}
	return m
}
