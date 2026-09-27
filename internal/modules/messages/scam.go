package messages

import (
	"regexp"
	"strings"
)

// Scam shield (ProjectRequirement §6.9). Rules look for the moves rental
// scammers in Ghana make: asking for Mobile Money, payment before a
// viewing, "booking" or "commitment" fees, moving the chat elsewhere, and
// pressure. A match never blocks a message — it adds a gentle warning for
// the person receiving it, and moderators see the flags.

// Rule is one pattern with the warning it earns.
type Rule struct {
	Key     string
	Pattern *regexp.Regexp
	Warning string
}

var Rules = []Rule{
	{"momo", regexp.MustCompile(`(?i)\b(momo|mobile\s*money|send\s+(the\s+)?(money|cash)|transfer\s+(the\s+)?(money|cash|fee|amount)|mtn\s+number|vodafone\s+cash|telecel\s+cash|airteltigo\s+money)\b`),
		"This message asks about sending money. Never pay rent or fees before you've seen the place and met the owner or verified agent."},
	{"pay_first", regexp.MustCompile(`(?i)\b(pay|paying|payment|deposit)\b[^.!?\n]{0,40}\b(before|first|to\s+(secure|reserve|hold|book))\b|\b(first|before)\b[^.!?\n]{0,25}\b(pay|payment)\b`),
		"Asking for payment before a viewing is a common scam. Pay nothing until you've seen the place in person."},
	{"booking_fee", regexp.MustCompile(`(?i)\b(booking|commitment|reservation|registration|form|processing|holding)\s+fee\b`),
		"RentMap never charges booking or commitment fees. Be careful with anyone who asks for one."},
	{"off_platform", regexp.MustCompile(`(?i)\b(whats\s*app|telegram|signal|imo)\b|\b(call|text|chat)\s+me\s+(on|at|via)\b`),
		"Keeping the conversation on RentMap protects you: we can help if something goes wrong."},
	{"pressure", regexp.MustCompile(`(?i)\b(urgent(ly)?|today\s+only|right\s+now|many\s+people\s+(are\s+)?(interested|calling|asking)|first\s+come|before\s+someone\s+else|last\s+chance|hurry)\b`),
		"Pressure to decide fast is a warning sign. Take your time and see the place first."},
}

// Check returns the keys of the rules a message trips.
func Check(body string) []string {
	var out []string
	for _, r := range Rules {
		if r.Pattern.MatchString(body) {
			out = append(out, r.Key)
		}
	}
	return out
}

// Warnings turns flags into the text shown to the recipient (one per rule,
// in rule order).
func Warnings(flags []string) []string {
	var out []string
	for _, r := range Rules {
		for _, f := range flags {
			if f == r.Key {
				out = append(out, r.Warning)
				break
			}
		}
	}
	return out
}

// phoneRe finds Ghanaian phone numbers however they're written:
// 024 123 4567, 0241234567, +233 24 123 4567, 233-24-1234567.
var phoneRe = regexp.MustCompile(`(?:\+?233[\s.-]?|0)[235][0-9](?:[\s.-]?[0-9]){7}`)

// HasPhone reports whether body contains a phone number.
func HasPhone(body string) bool { return phoneRe.MatchString(body) }

// MaskPhones hides phone numbers until the two sides have a confirmed
// viewing (§6.9: "Phone numbers are hidden until a viewing is confirmed").
func MaskPhones(body string) string {
	return phoneRe.ReplaceAllStringFunc(body, func(string) string { return "[number hidden until a viewing is confirmed]" })
}

// snippet is a one-line preview for the inbox.
func snippet(body string, masked bool, n int) string {
	if masked {
		body = MaskPhones(body)
	}
	body = strings.Join(strings.Fields(body), " ")
	if r := []rune(body); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return body
}
