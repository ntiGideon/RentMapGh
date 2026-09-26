// Package waitlist collects pre-launch sign-ups.
package waitlist

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/waitlistentry"
	"rentmapgh/internal/platform/phone"
)

// Area is a launch neighbourhood people can register interest in.
type Area struct{ Slug, Label string }

var Areas = []Area{
	{"ayeduase", "Ayeduase"},
	{"bomso", "Bomso"},
	{"kotei", "Kotei"},
	{"ayigya", "Ayigya"},
	{"oduom", "Oduom"},
	{"kentinkrono", "Kentinkrono"},
	{"kumasi-other", "Elsewhere in Kumasi"},
	{"accra", "Accra"},
}

func AreaLabel(slug string) string {
	for _, a := range Areas {
		if a.Slug == slug {
			return a.Label
		}
	}
	return ""
}

type Input struct {
	Name    string
	Phone   string
	Role    string
	Area    string
	Source  string
	Consent bool
}

// ValidationError maps form field → message.
type ValidationError map[string]string

func (v ValidationError) Error() string { return fmt.Sprintf("waitlist: %d invalid field(s)", len(v)) }

type Result struct {
	Phone   string // E.164
	Already bool
}

type Service struct{ db *ent.Client }

func NewService(db *ent.Client) *Service { return &Service{db: db} }

// Join validates and stores a sign-up. Signing up twice with the same phone is
// not an error: the caller shows "you're already on the list".
func (s *Service) Join(ctx context.Context, in Input) (Result, error) {
	in, e164, verr := validate(in)
	if verr != nil {
		return Result{}, verr
	}
	err := s.db.WaitlistEntry.Create().
		SetPhone(e164).
		SetName(in.Name).
		SetRole(waitlistentry.Role(in.Role)).
		SetArea(in.Area).
		SetSource(in.Source).
		SetConsent(in.Consent).
		Exec(ctx)
	if ent.IsConstraintError(err) {
		return Result{Phone: e164, Already: true}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("waitlist: create: %w", err)
	}
	return Result{Phone: e164}, nil
}

func validate(in Input) (Input, string, ValidationError) {
	errs := ValidationError{}

	in.Name = strings.Join(strings.Fields(in.Name), " ")
	if utf8.RuneCountInString(in.Name) > 80 {
		errs["name"] = "Please keep your name under 80 characters."
	}

	e164, err := phone.NormalizeGhana(in.Phone)
	if err != nil {
		errs["phone"] = err.Error()
	}

	switch in.Role {
	case "renter", "landlord", "agent":
	case "":
		in.Role = "renter"
	default:
		errs["form"] = "Please choose why you're joining."
	}

	if in.Area != "" && AreaLabel(in.Area) == "" {
		errs["area"] = "Please choose an area from the list."
	}
	if !in.Consent {
		errs["consent"] = "Tick this so we can tell you when we launch."
	}
	if len(in.Source) > 40 {
		in.Source = in.Source[:40]
	}

	if len(errs) > 0 {
		return in, "", errs
	}
	return in, e164, nil
}
