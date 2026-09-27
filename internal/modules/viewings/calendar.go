package viewings

import (
	"net/http"
	"strconv"
	"time"

	"rentmapgh/internal/ent/viewing"
	"rentmapgh/internal/platform/weekly"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

// Calendar hours shown (the viewing hours allowed: 6:00–20:00).
const calFrom, calTo = 6 * 60, 20 * 60

// Week is /viewings/calendar?week=YYYY-MM-DD: the lister's week.
func (h *Handler) Week(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	now := h.svc.Now().In(Accra)
	start := mondayOf(now)
	if t, err := time.ParseInLocation(time.DateOnly, r.URL.Query().Get("week"), Accra); err == nil {
		start = mondayOf(t)
	}
	end := start.AddDate(0, 0, 7)
	vs, err := h.svc.db.Viewing.Query().Where(viewing.ListerID(a.UserID), viewing.StartsAtGTE(start), viewing.StartsAtLT(end),
		viewing.StatusIn(viewing.StatusRequested, viewing.StatusProposed, viewing.StatusConfirmed, viewing.StatusCompleted)).
		Order(viewing.ByStartsAt()).All(r.Context())
	if h.fail(w, r, err) {
		return
	}
	hours, _ := h.svc.Hours(r.Context(), a.UserID)
	v := pages.CalendarView{
		Title: start.Format("2 Jan") + " – " + end.AddDate(0, 0, -1).Format("2 Jan 2006"),
		Prev:  "/viewings/calendar?week=" + start.AddDate(0, 0, -7).Format(time.DateOnly),
		Next:  "/viewings/calendar?week=" + end.Format(time.DateOnly),
		Today: "/viewings/calendar",
	}
	for hr := calFrom / 60; hr < calTo/60; hr++ {
		v.Hours = append(v.Hours, strconv.Itoa(hr)+":00")
	}
	for i := range 7 {
		d := start.AddDate(0, 0, i)
		day := pages.CalDay{Label: d.Format("Mon 2"), Today: d.Format(time.DateOnly) == now.Format(time.DateOnly)}
		for _, win := range hours {
			if win.Day == int(d.Weekday()) {
				day.Open = append(day.Open, pages.CalSlot{Top: pct(win.Start), Height: pctSpan(win.Start, win.End)})
			}
		}
		for _, vw := range vs {
			t := vw.StartsAt.In(Accra)
			if t.Format(time.DateOnly) != d.Format(time.DateOnly) {
				continue
			}
			m := t.Hour()*60 + t.Minute()
			ev := pages.CalEvent{URL: "/viewings/" + vw.ID.String(), Time: t.Format("15:04"), Status: string(vw.Status),
				Top: pct(m), Height: pctSpan(m, m+vw.DurationMin)}
			if p, err := h.svc.place(r.Context(), vw.ListingID); err == nil {
				ev.Title = headline(p)
			}
			if u, err := h.svc.db.User.Get(r.Context(), vw.RenterID); err == nil {
				ev.Who = nameOr(u)
			}
			day.Events = append(day.Events, ev)
		}
		v.Days = append(v.Days, day)
	}
	v.HoursText = weekly.Describe(hours)
	w.Header().Set("Cache-Control", "private, no-store")
	render.Component(w, r, http.StatusOK, pages.Calendar(layouts.Meta{Title: "Viewing calendar", NoIndex: true, Modules: []string{"js/calendar.js"}}, v))
}

func mondayOf(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, Accra)
	back := (int(d.Weekday()) + 6) % 7 // Monday = 0
	return d.AddDate(0, 0, -back)
}

// pct places a minute-of-day inside the calendar's hour range, as a CSS %.
func pct(m int) string {
	m = max(calFrom, min(calTo, m))
	return strconv.FormatFloat(float64(m-calFrom)*100/float64(calTo-calFrom), 'f', 2, 64) + "%"
}

func pctSpan(from, to int) string {
	from, to = max(calFrom, from), min(calTo, to)
	return strconv.FormatFloat(float64(max(to-from, 20))*100/float64(calTo-calFrom), 'f', 2, 64) + "%"
}
