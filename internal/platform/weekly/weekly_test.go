package weekly

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var accra = time.FixedZone("GMT", 0)

func TestNormalize(t *testing.T) {
	got := Normalize([]Window{
		{Day: 6, Start: 9 * 60, End: 11 * 60},
		{Day: 6, Start: 10 * 60, End: 13 * 60}, // overlaps: merged
		{Day: 0, Start: 14 * 60, End: 17 * 60},
		{Day: 2, Start: 5 * 60, End: 9 * 60},      // before 6:00: dropped
		{Day: 3, Start: 10 * 60, End: 10*60 + 15}, // under 30 min: dropped
		{Day: 9, Start: 10 * 60, End: 12 * 60},    // no such day
	})
	assert.Equal(t, []Window{{0, 14 * 60, 17 * 60}, {6, 9 * 60, 13 * 60}}, got)
	assert.Equal(t, "Sun 14:00–17:00, Sat 9:00–13:00", Describe(got))
}

func TestSlots(t *testing.T) {
	ws := []Window{{Day: 6, Start: 9 * 60, End: 10*60 + 30}} // Saturdays 9:00–10:30
	fri := time.Date(2026, 10, 2, 8, 0, 0, 0, accra)         // a Friday
	busy := time.Date(2026, 10, 3, 9, 30, 0, 0, accra)
	got := Slots(ws, accra, fri, fri.AddDate(0, 0, 9), fri, 30*time.Minute, func(t time.Time) bool { return t.Equal(busy) })
	assert.Equal(t, []time.Time{
		time.Date(2026, 10, 3, 9, 0, 0, 0, accra),
		time.Date(2026, 10, 3, 10, 0, 0, 0, accra),
		time.Date(2026, 10, 10, 9, 0, 0, 0, accra),
		time.Date(2026, 10, 10, 9, 30, 0, 0, accra),
		time.Date(2026, 10, 10, 10, 0, 0, 0, accra),
	}, got)

	// Slots before notBefore (the minimum notice) are skipped.
	sat := time.Date(2026, 10, 3, 8, 0, 0, 0, accra)
	got = Slots(ws, accra, sat, sat.AddDate(0, 0, 1), sat.Add(90*time.Minute), 30*time.Minute, nil)
	assert.Equal(t, []time.Time{time.Date(2026, 10, 3, 9, 30, 0, 0, accra), time.Date(2026, 10, 3, 10, 0, 0, 0, accra)}, got)
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]int{"9:00": 540, "09:30": 570, "13:00": 780, " 7:05 ": 425} {
		got, ok := ParseClock(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "9", "25:00", "9:60", "a:b"} {
		_, ok := ParseClock(bad)
		assert.False(t, ok, bad)
	}
	assert.Equal(t, "9:05", Clock(545))
}
