// Package trust computes each listing's trust score, 0–100
// (ProjectRequirement §6.10). Renters never see the number — they see the
// badges it's made of — but it orders "Recommended" results.
package trust

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/ent/report"
	"rentmapgh/internal/modules/listings"
	"rentmapgh/internal/modules/mandates"
	"rentmapgh/internal/modules/viewings"
)

// Weights, v1. Positive signals add up to 100 at most.
const (
	WIdentity     = 25
	WPhone        = 10
	WAccountAge   = 5  // older than 90 days
	WAuthority    = 20 // an owner's own listing, or an agent with a confirmed mandate (15) and licence (5)
	WRepliesFast  = 10
	WShowsUp      = 10
	WAccurate     = 10
	WFresh        = 5
	WNotAsDesc    = -10 // each "not as described", up to 3
	WReportUpheld = -15 // each report against the lister acted on in the past year
)

// Breakdown is a score with the signals behind it (for the admin view and tests).
type Breakdown struct {
	Score                                 int
	Identity, Phone, OldAccount           bool
	OwnerListing, Licensed, Mandated      bool
	RepliesFast, ShowsUp, Accurate, Fresh bool
	NotAsDescribed, ReportsUpheld         int
}

type Service struct {
	db       *ent.Client
	viewings *viewings.Service
	mandates *mandates.Service
	now      func() time.Time
}

func NewService(db *ent.Client, v *viewings.Service, m *mandates.Service) *Service {
	return &Service{db: db, viewings: v, mandates: m, now: func() time.Time { return time.Now().UTC() }}
}

// Compute scores one listing.
func (s *Service) Compute(ctx context.Context, l *ent.Listing) (Breakdown, error) {
	now := s.now()
	u, err := s.db.User.Get(ctx, l.ListerID)
	if err != nil {
		return Breakdown{}, fmt.Errorf("trust: lister: %w", err)
	}
	var b Breakdown
	b.Identity = u.IdentityVerifiedAt != nil
	b.Phone = u.PhoneVerifiedAt != nil
	b.OldAccount = now.Sub(u.CreatedAt) > 90*24*time.Hour
	if l.ListerKind == listing.ListerKindOwner {
		b.OwnerListing = true
	} else {
		b.Licensed = u.LicenseVerifiedAt != nil
		if s.mandates != nil {
			if unit, err := s.db.Unit.Get(ctx, l.UnitID); err == nil {
				m, _ := s.mandates.Latest(ctx, l.ListerID, unit.PropertyID)
				b.Mandated = mandates.StateOf(m, now).Confirmed()
			}
		}
	}
	rel, err := s.viewings.ReliabilityOf(ctx, l.ListerID)
	if err != nil {
		return Breakdown{}, err
	}
	b.RepliesFast, b.ShowsUp, b.Accurate = rel.RepliesFast(), rel.ShowsUp(), rel.Accurate()
	b.NotAsDescribed = min(rel.NotAsDesc, 3)
	b.Fresh = listings.Fresh(l, now)
	b.ReportsUpheld, err = s.db.Report.Query().Where(report.SubjectID(l.ListerID), report.StatusEQ(report.StatusActioned),
		report.ReasonNEQ("rented"), report.CreatedAtGT(now.Add(-365*24*time.Hour))).Count(ctx)
	if err != nil {
		return Breakdown{}, fmt.Errorf("trust: reports: %w", err)
	}

	add := func(ok bool, w int) {
		if ok {
			b.Score += w
		}
	}
	add(b.Identity, WIdentity)
	add(b.Phone, WPhone)
	add(b.OldAccount, WAccountAge)
	add(b.OwnerListing, WAuthority)
	add(b.Licensed, 5)
	add(b.Mandated, WAuthority-5)
	add(b.RepliesFast, WRepliesFast)
	add(b.ShowsUp, WShowsUp)
	add(b.Accurate, WAccurate)
	add(b.Fresh, WFresh)
	b.Score += b.NotAsDescribed*WNotAsDesc + b.ReportsUpheld*WReportUpheld
	b.Score = max(0, min(100, b.Score))
	return b, nil
}

// Refresh recomputes and stores one listing's score.
func (s *Service) Refresh(ctx context.Context, id uuid.UUID) (Breakdown, error) {
	l, err := s.db.Listing.Get(ctx, id)
	if err != nil {
		return Breakdown{}, fmt.Errorf("trust: listing: %w", err)
	}
	b, err := s.Compute(ctx, l)
	if err != nil {
		return b, err
	}
	if b.Score != l.TrustScore {
		if err := s.db.Listing.UpdateOne(l).SetTrustScore(b.Score).Exec(ctx); err != nil {
			return b, fmt.Errorf("trust: save: %w", err)
		}
	}
	return b, nil
}

// RefreshAll is the hourly job over every live listing. It returns how
// many scores changed.
func (s *Service) RefreshAll(ctx context.Context) (int, error) {
	ls, err := s.db.Listing.Query().Where(listing.StatusEQ(listing.StatusActive)).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("trust: all: %w", err)
	}
	changed := 0
	for _, l := range ls {
		b, err := s.Compute(ctx, l)
		if err != nil {
			continue
		}
		if b.Score != l.TrustScore {
			if err := s.db.Listing.UpdateOne(l).SetTrustScore(b.Score).Exec(ctx); err == nil {
				changed++
			}
		}
	}
	return changed, nil
}
