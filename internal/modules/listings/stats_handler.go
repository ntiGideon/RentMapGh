package listings

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

// Stats is /listings/{id}/stats: the last 30 days of one listing.
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	d, err := h.svc.Load(r.Context(), actor(r), id)
	if h.notFound(w, r, err) {
		return
	}
	series, err := h.svc.Series(r.Context(), id, 30)
	if err != nil {
		slog.ErrorContext(r.Context(), "listings: stats", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	var total Counts
	for _, s := range series {
		total.Views += s.Views
		total.Saves += s.Saves
		total.Contacts += s.Contacts
	}
	v := pages.StatsView{Title: Title(d), ListingURL: PublicPath(d), Views: total.Views, Saves: total.Saves, Contacts: total.Contacts,
		Rate: fmt.Sprintf("%.1f%%", total.ContactRate()), Chart: viewsChart(series)}
	if total.Views == 0 {
		v.Rate = "—"
	}
	for i := len(series) - 1; i >= 0; i-- {
		s := series[i]
		v.Rows = append(v.Rows, pages.StatRow{Day: s.Day.Format("Mon 2 Jan"), Views: s.Views, Saves: s.Saves, Contacts: s.Contacts})
	}
	render.Component(w, r, http.StatusOK, pages.Stats(layouts.Meta{Title: "Stats · " + v.Title, NoIndex: true,
		Modules: []string{"js/stats-chart.js"}}, v))
}

// Chart geometry (SVG user units; the SVG scales to its container).
const (
	chartW, chartH = 720, 220
	plotL, plotR   = 40, 712
	plotT, plotB   = 14, 186
	maxBar         = 14.0 // ≤ 24px even when stretched to a wide screen
	barRadius      = 4.0
)

// viewsChart lays out a column per day: 4px rounded tops, square at the
// baseline, clean ticks, and a hit area per day bigger than the bar.
func viewsChart(series []DayStat) pages.Chart {
	top := 0
	for _, s := range series {
		top = max(top, s.Views)
	}
	yMax := niceMax(top)
	c := pages.Chart{W: chartW, H: chartH, PlotL: plotL, PlotR: plotR, Base: plotB}
	for _, t := range []int{0, yMax / 2, yMax} {
		y := plotB - float64(t)/float64(yMax)*float64(plotB-plotT)
		c.Ticks = append(c.Ticks, pages.Tick{Y: f1(y), Label: strconv.Itoa(t)})
	}
	slot := float64(plotR-plotL) / float64(len(series))
	w := math.Min(maxBar, slot-2) // the 2px surface gap between neighbours
	for i, s := range series {
		x := float64(plotL) + slot*float64(i) + (slot-w)/2
		b := pages.Bar{HitX: f1(float64(plotL) + slot*float64(i)), HitW: f1(slot), Tip: s.Day.Format("Mon 2 Jan") + " · " + plural(s.Views, "view")}
		if s.Views > 0 {
			h := float64(s.Views) / float64(yMax) * float64(plotB-plotT)
			b.Path = roundedTop(x, plotB-h, w, h)
		}
		if i%7 == len(series)%7 || i == len(series)-1 { // a date label about once a week
			b.Label, b.LabelX, b.Anchor = s.Day.Format("2 Jan"), f1(x+w/2), "middle"
			if i == len(series)-1 {
				b.LabelX, b.Anchor = f1(x+w), "end"
			}
		}
		c.Bars = append(c.Bars, b)
	}
	return c
}

// roundedTop is a column path with rounded top corners and a square base.
func roundedTop(x, y, w, h float64) string {
	r := math.Min(barRadius, math.Min(w/2, h))
	return fmt.Sprintf("M%s %sV%sQ%s %s %s %sH%sQ%s %s %s %sV%sZ",
		f1(x), f1(y+h), f1(y+r), f1(x), f1(y), f1(x+r), f1(y), f1(x+w-r), f1(x+w), f1(y), f1(x+w), f1(y+r), f1(y+h))
}

// niceMax rounds a maximum up to a clean, even top tick (so the middle
// tick is whole): 4, 6, 8, 10, then 20, 50, 100, 200…
func niceMax(v int) int {
	if v <= 10 {
		return max(4, v+v%2)
	}
	mag := math.Pow(10, math.Floor(math.Log10(float64(v))))
	for _, m := range []float64{1, 2, 5, 10} {
		if float64(v) <= m*mag {
			return int(m * mag)
		}
	}
	return v
}

func f1(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }
