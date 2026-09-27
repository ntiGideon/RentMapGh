package messages

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheck(t *testing.T) {
	cases := map[string][]string{
		"Hello, is the room still available?":                          nil,
		"Yes, you can come on Saturday at 10.":                         nil,
		"Send the money to my MoMo and I'll hold the room":             {"momo"},
		"Please pay the deposit first before I show you":               {"pay_first"},
		"You need to pay to secure the room":                           {"pay_first"},
		"There is a GHS 200 commitment fee":                            {"booking_fee"},
		"Chat me on WhatsApp":                                          {"off_platform"},
		"Many people are interested, it's urgent":                      {"pressure"},
		"Send money now, booking fee, text me on telegram, today only": {"momo", "booking_fee", "off_platform", "pressure"},
		"I'll pay the rent when I move in":                             nil,
		"Is the water from a borehole?":                                nil,
	}
	for msg, want := range cases {
		assert.Equal(t, want, Check(msg), msg)
	}
	assert.Len(t, Warnings([]string{"pressure", "momo"}), 2)
	assert.Contains(t, Warnings([]string{"momo"})[0], "Never pay rent or fees")
}

func TestMaskPhones(t *testing.T) {
	for _, in := range []string{"call 024 123 4567", "0241234567 please", "+233 24 123 4567", "233-24-123-4567", "my number is 020.123.4567"} {
		assert.True(t, HasPhone(in), in)
		assert.NotRegexp(t, `\d{3}.?\d{4}`, MaskPhones(in), in)
		assert.Contains(t, MaskPhones(in), "[number hidden until a viewing is confirmed]")
	}
	for _, in := range []string{"rent is 1200 per month", "room 12, block 3", "GHS 4,500 a year", "2026-10-03"} {
		assert.False(t, HasPhone(in), in)
		assert.Equal(t, in, MaskPhones(in))
	}
	assert.Equal(t, "Hello there…", snippet("Hello   there\nfriend", false, 13))
}
