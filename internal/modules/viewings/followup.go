package viewings

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/report"
	"rentmapgh/internal/ent/viewing"
	"rentmapgh/internal/modules/audit"
)

// Follow-up (ProjectRequirement §6.8 steps 4, 6, 7): reminders 24 h and 2 h
// before, a feedback prompt after, and the reliability both sides earn.

const (
	FeedbackAfter    = 2 * time.Hour      // ask this long after the start
	FeedbackWindow   = 3 * 24 * time.Hour // …but not for viewings older than this
	NotAsDescribedAt = 2                  // reports in 90 days that open a moderation case
)

// Remind is the 10-minute job: 24 h and 2 h reminders to both sides, then
// the "how was it?" prompts. It returns how many notes went out.
func (s *Service) Remind(ctx context.Context) (int, error) {
	now := s.now()
	vs, err := s.db.Viewing.Query().Where(viewing.StatusEQ(viewing.StatusConfirmed),
		viewing.StartsAtGT(now.Add(-FeedbackWindow)), viewing.StartsAtLT(now.Add(24*time.Hour))).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("viewings: remind: %w", err)
	}
	sent := 0
	for _, v := range vs {
		until := v.StartsAt.Sub(now)
		switch {
		case until > 2*time.Hour && v.Reminded24At == nil:
			if s.claim(ctx, v, viewing.FieldReminded24At) {
				sent += s.remindBoth(ctx, v, "tomorrow", false)
			}
		case until > 0 && until <= 2*time.Hour && v.Reminded2At == nil:
			if s.claim(ctx, v, viewing.FieldReminded2At) {
				sent += s.remindBoth(ctx, v, "in 2 hours", true)
			}
		case until <= -FeedbackAfter && v.FeedbackAskedAt == nil:
			if s.claim(ctx, v, viewing.FieldFeedbackAskedAt) {
				sent += s.askFeedback(ctx, v)
			}
		}
	}
	return sent, nil
}

// claim sets a follow-up timestamp if it's still empty, so two job runs
// (or instances) never send the same reminder twice.
func (s *Service) claim(ctx context.Context, v *ent.Viewing, field string) bool {
	up := s.db.Viewing.Update().Where(viewing.ID(v.ID))
	now := s.now()
	switch field {
	case viewing.FieldReminded24At:
		up.Where(viewing.Reminded24AtIsNil()).SetReminded24At(now)
	case viewing.FieldReminded2At:
		up.Where(viewing.Reminded2AtIsNil()).SetReminded2At(now)
	case viewing.FieldFeedbackAskedAt:
		up.Where(viewing.FeedbackAskedAtIsNil()).SetFeedbackAskedAt(now)
	}
	n, err := up.Save(ctx)
	if err != nil {
		slog.WarnContext(ctx, "viewings: claim", "err", err)
	}
	return n == 1
}

func (s *Service) remindBoth(ctx context.Context, v *ent.Viewing, when string, urgent bool) int {
	p, err := s.place(ctx, v.ListingID)
	if err != nil {
		return 0
	}
	renter, err := s.db.User.Get(ctx, v.RenterID)
	if err != nil {
		return 0
	}
	at := v.StartsAt.In(Accra).Format("15:04")
	s.tell(ctx, renter, "viewing.reminder", "Viewing "+when+" at "+at+": "+headline(p), s.path(v), urgent,
		fmt.Sprintf("RentMap reminder: your viewing of %s is %s at %s. Directions: %s", unitLabel(p), when, at, s.link(v)))
	s.tell(ctx, p.Lister, "viewing.reminder", firstName(renter)+" is coming "+when+" at "+at, s.path(v), urgent,
		fmt.Sprintf("RentMap reminder: %s is viewing %s %s at %s. Details: %s", firstName(renter), unitLabel(p), when, at, s.link(v)))
	return 2
}

func (s *Service) askFeedback(ctx context.Context, v *ent.Viewing) int {
	p, err := s.place(ctx, v.ListingID)
	if err != nil {
		return 0
	}
	renter, err := s.db.User.Get(ctx, v.RenterID)
	if err != nil {
		return 0
	}
	s.tell(ctx, renter, "viewing.feedback", "How was the viewing of "+headline(p)+"?", s.path(v)+"/feedback", false,
		fmt.Sprintf("RentMap: how was your viewing of %s? Tell us in 20 seconds: %s/feedback", unitLabel(p), s.link(v)))
	s.tell(ctx, p.Lister, "viewing.outcome", "Did "+firstName(renter)+" come to the viewing?", s.path(v), false, "")
	return 2
}

// ── Renter feedback ──────────────────────────────────────────────────────

// Feedback is the renter's answer after a viewing.
type Feedback struct {
	Outcome    string // happened, renter_missed, lister_missed
	Accuracy   string // as_described, mostly, not_as_described (if it happened)
	Interested *bool
	Note       string
}

// GiveFeedback records the renter's feedback once, after the start time.
// Two "not as described" reports on one listing within 90 days open a case
// for moderators (§6.8 step 6).
func (s *Service) GiveFeedback(ctx context.Context, a Actor, id uuid.UUID, f Feedback) (*Detail, error) {
	d, err := s.Load(ctx, a, id)
	if err != nil {
		return nil, err
	}
	v := d.V
	switch {
	case d.Role != "renter":
		return nil, ErrNotFound
	case v.FeedbackAt != nil:
		return nil, ValidationError{"form": "Thanks — you've already told us about this viewing."}
	case v.StartsAt.After(s.now()):
		return nil, ValidationError{"form": "You can leave feedback after the viewing time."}
	case v.Status != viewing.StatusConfirmed && v.Status != viewing.StatusCompleted && v.Status != viewing.StatusNoShow:
		return nil, ValidationError{"form": "This viewing didn't go ahead, so there's nothing to rate."}
	}
	up := s.db.Viewing.UpdateOne(v).SetFeedbackAt(s.now())
	switch f.Outcome {
	case "happened":
		up.SetRenterOutcome(viewing.RenterOutcomeHappened)
		switch f.Accuracy {
		case "as_described":
			up.SetAccuracy(viewing.AccuracyAsDescribed)
		case "mostly":
			up.SetAccuracy(viewing.AccuracyMostly)
		case "not_as_described":
			up.SetAccuracy(viewing.AccuracyNotAsDescribed)
		default:
			return nil, ValidationError{"accuracy": "Was the place as described?"}
		}
		if v.Status == viewing.StatusConfirmed {
			up.SetStatus(viewing.StatusCompleted)
		}
	case "renter_missed":
		up.SetRenterOutcome(viewing.RenterOutcomeRenterMissed)
	case "lister_missed":
		up.SetRenterOutcome(viewing.RenterOutcomeListerMissed)
	default:
		return nil, ValidationError{"outcome": "Did the viewing happen?"}
	}
	if f.Interested != nil {
		up.SetInterested(*f.Interested)
	}
	note := strings.TrimSpace(f.Note)
	if utf8.RuneCountInString(note) > 500 {
		note = string([]rune(note)[:500])
	}
	up.SetFeedbackNote(note)
	if _, err := up.Save(ctx); err != nil {
		return nil, fmt.Errorf("viewings: feedback: %w", err)
	}
	s.record(ctx, a, "viewing.feedback", v, map[string]any{"outcome": f.Outcome, "accuracy": f.Accuracy})
	if f.Accuracy == "not_as_described" {
		s.maybeOpenCase(ctx, a, d)
	}
	return s.Load(ctx, a, id)
}

// maybeOpenCase opens a moderation report once a listing collects enough
// "not as described" feedback.
func (s *Service) maybeOpenCase(ctx context.Context, a Actor, d *Detail) {
	n, err := s.db.Viewing.Query().Where(viewing.ListingID(d.V.ListingID), viewing.AccuracyEQ(viewing.AccuracyNotAsDescribed),
		viewing.FeedbackAtGT(s.now().Add(-90*24*time.Hour))).Count(ctx)
	if err != nil || n < NotAsDescribedAt {
		return
	}
	open, err := s.db.Report.Query().Where(report.TargetTypeEQ(report.TargetTypeListing), report.TargetID(d.V.ListingID),
		report.ReasonEQ("not_as_described"), report.StatusEQ(report.StatusOpen)).Exist(ctx)
	if err != nil || open {
		return
	}
	if err := s.db.Report.Create().SetReporterID(a.UserID).SetTargetType(report.TargetTypeListing).SetTargetID(d.V.ListingID).
		SetSubjectID(d.V.ListerID).SetReason("not_as_described").
		SetNote(fmt.Sprintf("%d renters said this place wasn't as described (automatic).", n)).Exec(ctx); err != nil {
		slog.WarnContext(ctx, "viewings: open case", "err", err)
		return
	}
	s.audit.Record(ctx, audit.Event{Action: "listing.flagged_not_as_described", TargetType: "listing", TargetID: d.V.ListingID.String(),
		Meta: map[string]any{"count": n}})
}

// ── Reliability ──────────────────────────────────────────────────────────

// Reliability is how a user shows up for viewings, over the last 180 days.
type Reliability struct {
	Requests, AnsweredInDay int // as lister: requests, and those answered within 24 h
	Held, ListerMissed      int // as lister: past confirmed viewings, and renter reports that the lister didn't come
	AsDescribed, NotAsDesc  int // as lister: accuracy ratings
	Attended, RenterMissed  int // as renter: viewings they came to / missed
}

// RepliesFast: most requests answered within a day (3+ requests).
func (r Reliability) RepliesFast() bool {
	return r.Requests >= 3 && r.AnsweredInDay*10 >= r.Requests*8
}

// ShowsUp: 3+ viewings held and none missed by the lister.
func (r Reliability) ShowsUp() bool { return r.Held >= 3 && r.ListerMissed == 0 }

// Accurate: 2+ "as described" and no "not as described".
func (r Reliability) Accurate() bool { return r.AsDescribed >= 2 && r.NotAsDesc == 0 }

// ReliabilityOf computes a user's reliability, both as lister and renter.
func (s *Service) ReliabilityOf(ctx context.Context, userID uuid.UUID) (Reliability, error) {
	since := s.now().Add(-180 * 24 * time.Hour)
	vs, err := s.db.Viewing.Query().Where(viewing.Or(viewing.ListerID(userID), viewing.RenterID(userID)),
		viewing.CreatedAtGT(since)).All(ctx)
	if err != nil {
		return Reliability{}, fmt.Errorf("viewings: reliability: %w", err)
	}
	var r Reliability
	now := s.now()
	for _, v := range vs {
		if v.ListerID == userID {
			if v.CreatedAt.Before(now.Add(-24 * time.Hour)) {
				r.Requests++
				if v.RespondedAt != nil && v.RespondedAt.Sub(v.CreatedAt) <= 24*time.Hour {
					r.AnsweredInDay++
				}
			}
			if v.StartsAt.Before(now) && (v.Status == viewing.StatusCompleted || v.Status == viewing.StatusNoShow) {
				r.Held++
			}
			if v.RenterOutcome != nil && *v.RenterOutcome == viewing.RenterOutcomeListerMissed {
				r.ListerMissed++
			}
			if v.Accuracy != nil {
				switch *v.Accuracy {
				case viewing.AccuracyAsDescribed:
					r.AsDescribed++
				case viewing.AccuracyNotAsDescribed:
					r.NotAsDesc++
				}
			}
		}
		if v.RenterID == userID && v.StartsAt.Before(now) {
			switch {
			case v.Status == viewing.StatusNoShow || (v.RenterOutcome != nil && *v.RenterOutcome == viewing.RenterOutcomeRenterMissed):
				r.RenterMissed++
			case v.Status == viewing.StatusCompleted:
				r.Attended++
			}
		}
	}
	return r, nil
}
