package listings

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNiceMax(t *testing.T) {
	for in, want := range map[int]int{0: 4, 3: 4, 5: 6, 7: 8, 10: 10, 11: 20, 38: 50, 51: 100, 120: 200, 999: 1000} {
		assert.Equal(t, want, niceMax(in), in)
	}
}

func TestViewsChart(t *testing.T) {
	series := make([]DayStat, 30)
	for i := range series {
		series[i].Day = time.Date(2026, 9, 1+i%28, 0, 0, 0, 0, time.UTC)
	}
	series[29].Views = 7
	c := viewsChart(series)
	assert.Len(t, c.Bars, 30)
	assert.Empty(t, c.Bars[0].Path, "no bar for a zero day")
	assert.NotEmpty(t, c.Bars[29].Path)
	assert.Equal(t, "8", c.Ticks[2].Label)
	assert.Contains(t, c.Bars[29].Tip, "7 views")
	assert.Equal(t, "M10 20V14Q10 10 14 10H16Q20 10 20 14V20Z", roundedTop(10, 10, 10, 10), "4px top corners, square base")
}

func TestContactRate(t *testing.T) {
	assert.Zero(t, Counts{Contacts: 3}.ContactRate())
	assert.InDelta(t, 25.0, Counts{Views: 8, Contacts: 2}.ContactRate(), 0.001)
}
