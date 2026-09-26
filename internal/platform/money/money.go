// Package money handles Ghana cedi amounts. Money is always an int64 number
// of pesewas (GH₵1 = 100 pesewas); floats never touch it.
package money

import (
	"errors"
	"strconv"
	"strings"
)

// Pesewas is an amount in pesewas.
type Pesewas int64

// Fee is an extra one-off charge with its own label ("Sanitation", "Key deposit").
type Fee struct {
	Label  string  `json:"label"`
	Amount Pesewas `json:"amount"`
}

// Max is a sanity ceiling for a single fee or rent (GH₵ 10 million).
const Max Pesewas = 10_000_000_00

var ErrInvalid = errors.New("Enter an amount in cedis, like 1,200 or 850.50.")

// Parse reads what people type: "1200", "1,200", "GH₵ 1,200.50", "₵850".
// An empty string is an error; callers decide whether blank means "not answered".
func Parse(raw string) (Pesewas, error) {
	s := strings.TrimSpace(raw)
	for _, p := range []string{"GH₵", "GHS", "GHC", "₵"} {
		s = strings.TrimPrefix(strings.ToUpper(s), p)
	}
	s = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), " ", "")
	if s == "" {
		return 0, ErrInvalid
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if hasDot && (len(frac) == 0 || len(frac) > 2) {
		return 0, ErrInvalid
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w < 0 {
		return 0, ErrInvalid
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, ErrInvalid
	}
	if w > int64(Max/100) {
		return 0, ErrInvalid
	}
	return Pesewas(w*100 + f), nil
}

// String renders "GH₵ 1,200" (pesewas only when there are any: "GH₵ 850.50").
func (p Pesewas) String() string { return "GH₵ " + p.Number() }

// Number renders "1,200" / "850.50" without the currency sign.
func (p Pesewas) Number() string {
	neg := p < 0
	if neg {
		p = -p
	}
	whole := strconv.FormatInt(int64(p/100), 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	out := b.String()
	if c := p % 100; c != 0 {
		out += "." + strconv.FormatInt(int64(c)/10, 10) + strconv.FormatInt(int64(c)%10, 10)
	}
	if neg {
		return "-" + out
	}
	return out
}

// Input renders the value for a form field ("1200" / "850.50").
func (p Pesewas) Input() string {
	return strings.ReplaceAll(p.Number(), ",", "")
}
