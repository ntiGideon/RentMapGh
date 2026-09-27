package listings

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent/listing"
	"rentmapgh/internal/platform/money"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

// MaxCompare is how many places fit side by side.
const MaxCompare = 4

// Compare shows up to four live listings side by side:
// /compare?ids=a,b,c. The tray (compare.js) builds the link.
func (h *Handler) Compare(w http.ResponseWriter, r *http.Request) {
	var ids []uuid.UUID
	for _, raw := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id, err := uuid.Parse(strings.TrimSpace(raw)); err == nil && len(ids) < MaxCompare {
			ids = append(ids, id)
		}
	}
	items, err := h.svc.loadPublic(r.Context(), ids)
	if err != nil {
		slog.ErrorContext(r.Context(), "compare", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	var live []*Item
	for _, d := range items {
		if d.L.Status == listing.StatusActive {
			live = append(live, d)
		}
	}
	v := pages.CompareView{Cards: h.cards(r.Context(), live, Filter{}.Ref(), h.saved.read(r))}
	if len(live) >= 2 {
		v.Rows = compareRows(live, time.Now().UTC())
	}
	m := layouts.Meta{Title: "Compare places", NoIndex: true, Modules: []string{"js/compare.js"}}
	w.Header().Set("Cache-Control", "private, no-cache")
	render.Component(w, r, http.StatusOK, pages.Compare(m, v))
}

func compareRows(items []*Item, now time.Time) []pages.CompareRow {
	var rows []pages.CompareRow
	add := func(label string, f func(d *Item) string) {
		row := pages.CompareRow{Label: label, Best: make([]bool, len(items))}
		for _, d := range items {
			row.Values = append(row.Values, orDash(f(d)))
		}
		rows = append(rows, row)
	}
	// addMoney marks the lowest amount (ties all marked).
	addMoney := func(label string, f func(d *Item) *money.Pesewas) {
		row := pages.CompareRow{Label: label, Best: make([]bool, len(items))}
		var low *money.Pesewas
		for _, d := range items {
			p := f(d)
			if p == nil {
				row.Values = append(row.Values, "—")
				continue
			}
			row.Values = append(row.Values, p.String())
			if low == nil || *p < *low {
				low = p
			}
		}
		for i, d := range items {
			if p := f(d); p != nil && low != nil && *p == *low {
				row.Best[i] = true
			}
		}
		rows = append(rows, row)
	}
	addMoney("Per month", func(d *Item) *money.Pesewas { return d.T.MonthlyEquivalent })
	add("Rent", func(d *Item) string {
		if d.T.Rent == nil {
			return ""
		}
		p, _ := rentPeriod(d.T.RentPeriod)
		return d.T.Rent.String() + " / " + p.Per
	})
	addMoney("Total to move in", func(d *Item) *money.Pesewas {
		if m := ComputeMoveIn(d.Terms()); m.Complete {
			return &m.Total
		}
		return nil
	})
	add("Advance", func(d *Item) string {
		if d.T.AdvancePeriods == nil {
			return ""
		}
		p, _ := rentPeriod(d.T.RentPeriod)
		n := *d.T.AdvancePeriods
		if n == 1 {
			return "1 " + p.Per
		}
		return strconv.Itoa(n) + " " + p.Per + "s"
	})
	add("Type", func(d *Item) string { return UnitTypeLabel(d.U.UnitType) })
	add("Area", area)
	add("Self-contained", func(d *Item) string { return yesNo(d.U.SelfContained) })
	add("Furnishing", func(d *Item) string { return OptionLabel(FurnishedOptions, d.U.Furnished) })
	add("Electricity", func(d *Item) string { return OptionLabel(MeterOptions, d.U.MeterType) })
	add("Water", func(d *Item) string { return OptionLabel(WaterOptions, d.U.WaterSource) })
	add("Kitchen", func(d *Item) string { return OptionLabel(KitchenOptions, d.U.Kitchen) })
	add("Included", func(d *Item) string {
		var names []string
		for _, a := range Amenities {
			for _, k := range d.U.Amenities {
				if a.Key == k {
					names = append(names, a.Label)
				}
			}
		}
		return strings.Join(names, ", ")
	})
	add("Listed by", func(d *Item) string {
		if d.L.ListerKind == listing.ListerKindAgent {
			return "Agent"
		}
		return "Owner"
	})
	add("Availability", func(d *Item) string { s, _ := freshness(d, now); return s })
	return rows
}
