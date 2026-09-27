package viewings

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCalendarPlacement(t *testing.T) {
	sun := time.Date(2026, 9, 27, 15, 0, 0, 0, Accra)
	assert.Equal(t, "2026-09-21", mondayOf(sun).Format(time.DateOnly))
	assert.Equal(t, "2026-09-21", mondayOf(time.Date(2026, 9, 21, 0, 0, 0, 0, Accra)).Format(time.DateOnly))
	assert.Equal(t, "0.00%", pct(6*60))
	assert.Equal(t, "50.00%", pct(13*60))
	assert.Equal(t, "100.00%", pct(23*60), "clamped")
	assert.Equal(t, "3.57%", pctSpan(9*60, 9*60+30))
	assert.Equal(t, "2.38%", pctSpan(9*60, 9*60+5), "a short viewing still gets a readable block")
}
