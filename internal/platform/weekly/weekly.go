// Package weekly models recurring weekly time windows ("Saturdays
// 9:00–13:00") and cuts them into bookable slots.
package weekly

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Window is one weekly opening, in local time. Day is time.Weekday
// (0 = Sunday); Start and End are minutes after midnight.
type Window struct {
	Day   int `json:"day"`
	Start int `json:"start"`
	End   int `json:"end"`
}

// Valid reports whether the window is a sensible daytime range.
func (w Window) Valid() bool {
	return w.Day >= 0 && w.Day <= 6 && w.Start >= 6*60 && w.End <= 20*60 && w.End-w.Start >= 30
}

// Clock renders minutes after midnight as "9:00", "13:30".
func Clock(m int) string { return fmt.Sprintf("%d:%02d", m/60, m%60) }

// ParseClock reads "9:00", "09:30" or "13:00" as minutes after midnight.
func ParseClock(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// Normalize sorts windows, drops invalid ones and merges overlaps.
func Normalize(ws []Window) []Window {
	var ok []Window
	for _, w := range ws {
		if w.Valid() {
			ok = append(ok, w)
		}
	}
	sort.Slice(ok, func(i, j int) bool {
		if ok[i].Day != ok[j].Day {
			return ok[i].Day < ok[j].Day
		}
		return ok[i].Start < ok[j].Start
	})
	var out []Window
	for _, w := range ok {
		if n := len(out); n > 0 && out[n-1].Day == w.Day && w.Start <= out[n-1].End {
			out[n-1].End = max(out[n-1].End, w.End)
			continue
		}
		out = append(out, w)
	}
	return out
}

// Slots cuts the windows into slots of length step, from `from` until
// `until`, in loc. Slots starting before notBefore are skipped, as are
// those for which busy returns true.
func Slots(ws []Window, loc *time.Location, from, until, notBefore time.Time, step time.Duration, busy func(time.Time) bool) []time.Time {
	var out []time.Time
	day := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, loc)
	for ; day.Before(until); day = day.AddDate(0, 0, 1) {
		for _, w := range ws {
			if int(day.Weekday()) != w.Day {
				continue
			}
			for m := w.Start; m+int(step.Minutes()) <= w.End; m += int(step.Minutes()) {
				t := day.Add(time.Duration(m) * time.Minute)
				if t.Before(notBefore) || !t.Before(until) || (busy != nil && busy(t)) {
					continue
				}
				out = append(out, t)
			}
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return out
}

// DayNames are the weekday names, Sunday first (time.Weekday order).
var DayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// Describe renders windows for people: "Sat 9:00–13:00, Sun 14:00–17:00".
func Describe(ws []Window) string {
	var parts []string
	for _, w := range ws {
		parts = append(parts, DayNames[w.Day][:3]+" "+Clock(w.Start)+"–"+Clock(w.End))
	}
	return strings.Join(parts, ", ")
}
