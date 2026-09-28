package admin

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/duplicatecandidate"
	"rentmapgh/internal/modules/listings"
)

// Duplicate detection v1 (§6.11): pairs of listings whose exact pins are
// within 150 m, by different listers (or the same unit twice), that share
// a near-identical photo (pHash ≤ 10 bits apart) or — with no photo match —
// the same unit type and rent within 30 m. Headline similarity (pg_trgm)
// only adds to the score. Moderators decide; nothing is merged or removed
// automatically.
const scanSQL = `
WITH live AS (
	SELECT l.id, l.lister_id, l.unit_id, coalesce(l.headline, '') AS headline, u.unit_type, t.rent,
	       ST_SetSRID(ST_MakePoint(p.lng, p.lat), 4326)::geography AS g, p.approx_geog
	FROM listings l
	JOIN units u ON u.id = l.unit_id
	JOIN properties p ON p.id = u.property_id
	LEFT JOIN listing_terms t ON t.listing_id = l.id
	WHERE l.status IN ('active', 'paused', 'pending_review') AND p.lat IS NOT NULL AND p.approx_geog IS NOT NULL
), pairs AS (
	SELECT a.id AS a_id, b.id AS b_id,
	       round(ST_Distance(a.g, b.g))::int AS dist,
	       similarity(a.headline, b.headline) AS sim,
	       (a.unit_type <> '' AND a.unit_type = b.unit_type AND a.rent IS NOT NULL AND a.rent = b.rent) AS same_terms,
	       (SELECT min(bit_count((ma.phash # mb.phash)::bit(64)))
	          FROM listing_media ma JOIN listing_media mb ON mb.listing_id = b.id AND mb.status = 'ready'
	         WHERE ma.listing_id = a.id AND ma.status = 'ready') AS bits
	FROM live a
	JOIN live b ON a.id < b.id
	 AND ST_DWithin(a.approx_geog, b.approx_geog, 1000) -- public points sit 150–400 m off: a cheap, indexed pre-filter
	 AND ST_DWithin(a.g, b.g, 150)
	WHERE NOT (a.lister_id = b.lister_id AND a.unit_id <> b.unit_id) -- one lister's different rooms in a house
)
SELECT a_id, b_id, dist, bits, sim, same_terms FROM pairs
WHERE bits <= 10 OR (same_terms AND dist <= 30)`

// dupScore ranks a pair out of 100.
func dupScore(dist int, bits *int, sim float64, sameTerms bool) int {
	s := 0
	switch {
	case bits != nil && *bits <= 4:
		s += 60
	case bits != nil && *bits <= 10:
		s += 45
	}
	switch {
	case dist <= 30:
		s += 20
	case dist <= 150:
		s += 10
	}
	if sameTerms {
		s += 15
	}
	if sim >= 0.5 {
		s += 10
	}
	return min(s, 100)
}

// ScanDuplicates refreshes the candidates (hourly job). Decided pairs keep
// their decision; open pairs whose listings went offline are closed.
func (s *Service) ScanDuplicates(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, scanSQL)
	if err != nil {
		return 0, fmt.Errorf("duplicates: scan: %w", err)
	}
	type found struct {
		a, b      uuid.UUID
		dist      int
		bits      *int
		sim       float64
		sameTerms bool
	}
	var fs []found
	for rows.Next() {
		var f found
		if err := rows.Scan(&f.a, &f.b, &f.dist, &f.bits, &f.sim, &f.sameTerms); err != nil {
			rows.Close()
			return 0, fmt.Errorf("duplicates: scan row: %w", err)
		}
		fs = append(fs, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("duplicates: scan rows: %w", err)
	}
	for _, f := range fs {
		c := s.db.DuplicateCandidate.Create().SetListingA(f.a).SetListingB(f.b).SetDistanceM(f.dist).SetNillablePhotoBits(f.bits).
			SetTextSimilarity(f.sim).SetScore(dupScore(f.dist, f.bits, f.sim, f.sameTerms))
		err := c.OnConflictColumns(duplicatecandidate.FieldListingA, duplicatecandidate.FieldListingB).
			Update(func(u *ent.DuplicateCandidateUpsert) {
				u.UpdateDistanceM().UpdatePhotoBits().UpdateTextSimilarity().UpdateScore().UpdateUpdatedAt()
			}).Exec(ctx)
		if err != nil {
			return 0, fmt.Errorf("duplicates: save: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE duplicate_candidates d SET status = 'dismissed', updated_at = now()
		WHERE d.status = 'open' AND EXISTS (
			SELECT 1 FROM listings l WHERE l.id IN (d.listing_a, d.listing_b)
			AND l.status NOT IN ('active', 'paused', 'pending_review'))`); err != nil {
		return 0, fmt.Errorf("duplicates: close: %w", err)
	}
	return len(fs), nil
}

// DupPair is an open candidate with both listings, for review.
type DupPair struct {
	C    *ent.DuplicateCandidate
	A, B *listings.Item
	LA   *ent.User // listers
	LB   *ent.User
}

// Duplicates lists open candidates, strongest first.
func (s *Service) Duplicates(ctx context.Context) ([]DupPair, error) {
	cs, err := s.db.DuplicateCandidate.Query().Where(duplicatecandidate.StatusEQ(duplicatecandidate.StatusOpen)).
		Order(ent.Desc(duplicatecandidate.FieldScore), ent.Asc(duplicatecandidate.FieldCreatedAt)).Limit(50).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("duplicates: list: %w", err)
	}
	var out []DupPair
	for _, c := range cs {
		p := DupPair{C: c}
		if p.A, p.LA, err = s.listings.ForReview(ctx, c.ListingA); err != nil {
			continue
		}
		if p.B, p.LB, err = s.listings.ForReview(ctx, c.ListingB); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Service) DuplicateCount(ctx context.Context) int {
	n, _ := s.db.DuplicateCandidate.Query().Where(duplicatecandidate.StatusEQ(duplicatecandidate.StatusOpen)).Count(ctx)
	return n
}

// DecideDuplicate closes a candidate: "dismiss" (not the same place), or
// "remove_a" / "remove_b" (take that listing down as the copy).
func (s *Service) DecideDuplicate(ctx context.Context, a Actor, id uuid.UUID, decision, reason string) error {
	c, err := s.db.DuplicateCandidate.Get(ctx, id)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if c.Status != duplicatecandidate.StatusOpen {
		return Problem("Someone already decided this pair.")
	}
	st := duplicatecandidate.StatusActioned
	switch decision {
	case "dismiss":
		st = duplicatecandidate.StatusDismissed
	case "remove_a", "remove_b":
		victim := c.ListingA
		if decision == "remove_b" {
			victim = c.ListingB
		}
		if reason == "" {
			reason = "It duplicates another listing of the same place."
		}
		if err := s.RemoveListing(ctx, a, victim, reason); err != nil {
			return err
		}
	default:
		return Problem("Choose what to do with this pair.")
	}
	n, err := s.db.DuplicateCandidate.Update().Where(duplicatecandidate.ID(id), duplicatecandidate.StatusEQ(duplicatecandidate.StatusOpen)).
		SetStatus(st).SetHandledBy(a.UserID).SetHandledAt(s.now()).Save(ctx)
	if err != nil {
		return fmt.Errorf("duplicates: decide: %w", err)
	}
	if n == 0 {
		return Problem("Someone already decided this pair.")
	}
	s.audit.Record(ctx, a.event("admin.duplicate_"+decision, "duplicate", id.String(),
		map[string]any{"listing_a": c.ListingA.String(), "listing_b": c.ListingB.String()}))
	return nil
}
