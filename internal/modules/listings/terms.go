package listings

import "rentmapgh/internal/platform/money"

// Fee is an extra labelled one-off charge.
type Fee = money.Fee

// Terms are the money side of a listing. Pointer fields are "not answered
// yet": a listing can't be published until every one has a value, even if
// that value is zero (ProjectRequirement §6.4 — honesty enforced by the form).
type Terms struct {
	Rent           *money.Pesewas
	Period         string // RentPeriods key
	AdvancePeriods *int   // how many periods are paid up front
	Deposit        *money.Pesewas
	AgentFee       *money.Pesewas
	ServiceCharge  *money.Pesewas
	ViewingFee     *money.Pesewas // paid at viewing, not part of move-in
	OtherFees      []Fee
}

// Line is one row of a cost breakdown.
type Line struct {
	Label  string
	Amount money.Pesewas
}

// MoveIn is what a renter pays to get the keys.
type MoveIn struct {
	Lines        []Line
	Total        money.Pesewas
	Monthly      money.Pesewas // rent expressed per month
	Complete     bool          // every fee answered
	Missing      []string      // labels still unanswered
	UpfrontLabel string        // "6 months" / "1 academic year"
}

// ComputeMoveIn is the single move-in cost function used everywhere (card,
// detail page, compare, alerts, the wizard's live preview).
func ComputeMoveIn(t Terms) MoveIn {
	var m MoveIn
	p, okPeriod := rentPeriod(t.Period)
	if t.Rent != nil && okPeriod {
		m.Monthly = MonthlyEquivalent(*t.Rent, p)
	}

	if t.Rent == nil || !okPeriod {
		m.Missing = append(m.Missing, "Rent")
	}
	if t.AdvancePeriods == nil {
		m.Missing = append(m.Missing, "Rent paid upfront")
	}
	if t.Rent != nil && okPeriod && t.AdvancePeriods != nil {
		n := *t.AdvancePeriods
		m.UpfrontLabel = plural(n, p.Per)
		m.Lines = append(m.Lines, Line{"Rent × " + m.UpfrontLabel + " upfront", *t.Rent * money.Pesewas(n)})
	}
	add := func(label string, v *money.Pesewas) {
		if v == nil {
			m.Missing = append(m.Missing, label)
			return
		}
		if *v > 0 {
			m.Lines = append(m.Lines, Line{label, *v})
		}
	}
	add("Deposit", t.Deposit)
	add("Agent fee", t.AgentFee)
	add("Service charge", t.ServiceCharge)
	for _, f := range t.OtherFees {
		if f.Amount > 0 {
			m.Lines = append(m.Lines, Line{f.Label, f.Amount})
		}
	}
	if t.ViewingFee == nil {
		m.Missing = append(m.Missing, "Viewing fee")
	}
	for _, l := range m.Lines {
		m.Total += l.Amount
	}
	m.Complete = len(m.Missing) == 0
	return m
}

// MonthlyEquivalent is rent spread over the months one payment covers,
// rounded to the nearest cedi (filters compare these, not raw rents).
func MonthlyEquivalent(rent money.Pesewas, p RentPeriod) money.Pesewas {
	if p.Months <= 1 {
		return rent
	}
	per := int64(rent) / int64(p.Months)
	return money.Pesewas((per + 50) / 100 * 100)
}

func plural(n int, unit string) string {
	s := itoa(n) + " " + unit
	if n != 1 {
		switch unit {
		case "academic year":
			s = itoa(n) + " academic years"
		default:
			s += "s"
		}
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
