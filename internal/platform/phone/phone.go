// Package phone normalises and masks Ghanaian mobile numbers.
package phone

import (
	"errors"
	"strings"

	"github.com/nyaruka/phonenumbers"
)

// Errors are user-facing copy, shown under the phone field.
var (
	ErrEmpty     = errors.New("Enter your phone number.")
	ErrInvalid   = errors.New("That doesn't look like a Ghana number. Try 024 123 4567.")
	ErrNotMobile = errors.New("Please use a mobile number so we can text you.")
)

// NormalizeGhana accepts local ("024 123 4567", "241234567") and
// international ("+233 24 123 4567", "00233…") forms and returns E.164.
// Only Ghanaian mobile numbers are accepted (also limits SMS-pumping abuse).
func NormalizeGhana(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrEmpty
	}
	digits := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '+' {
			return r
		}
		return -1
	}, raw)
	digits = strings.Replace(digits, "00233", "+233", 1)
	if len(digits) == 9 && digits[0] != '0' { // typed after the "+233" prefix
		digits = "0" + digits
	}

	num, err := phonenumbers.Parse(digits, "GH")
	if err != nil || !phonenumbers.IsValidNumberForRegion(num, "GH") {
		return "", ErrInvalid
	}
	switch phonenumbers.GetNumberType(num) {
	case phonenumbers.MOBILE, phonenumbers.FIXED_LINE_OR_MOBILE:
	default:
		return "", ErrNotMobile
	}
	return phonenumbers.Format(num, phonenumbers.E164), nil
}

// Mask renders +233241234567 as "+233 24 *** 4567".
func Mask(e164 string) string {
	if len(e164) != 13 || !strings.HasPrefix(e164, "+233") {
		return e164
	}
	return "+233 " + e164[4:6] + " *** " + e164[9:]
}

// Pretty renders +233241234567 as "024 123 4567".
func Pretty(e164 string) string {
	if len(e164) != 13 || !strings.HasPrefix(e164, "+233") {
		return e164
	}
	return "0" + e164[4:6] + " " + e164[6:9] + " " + e164[9:]
}
