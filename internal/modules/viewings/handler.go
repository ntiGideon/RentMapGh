package viewings

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/viewing"
	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/platform/phone"
	"rentmapgh/internal/platform/weekly"
	"rentmapgh/internal/server/htmx"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

type Handler struct {
	svc      *Service
	photoURL func(listingID uuid.UUID) string // cover thumbnail, "" if none
}

// NewHandler: photoURL gives a listing's cover thumbnail for the cards.
func NewHandler(svc *Service, photoURL func(uuid.UUID) string) *Handler {
	return &Handler{svc: svc, photoURL: photoURL}
}

func actor(r *http.Request) Actor {
	v := reqctx.CurrentViewer(r.Context())
	return Actor{UserID: v.UserID, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		render.Error(w, r, http.StatusNotFound)
	case errors.Is(err, ErrForbidden):
		render.Forbidden(w, r, "This is your own listing", "You can't book a viewing of a place you listed.")
	default:
		slog.ErrorContext(r.Context(), "viewings", "err", err)
		render.Error(w, r, http.StatusInternalServerError)
	}
	return true
}

// ── Booking ──────────────────────────────────────────────────────────────

// RequestPage is the booking form, /l/{id}/viewing.
func (h *Handler) RequestPage(w http.ResponseWriter, r *http.Request) {
	h.renderRequest(w, r, http.StatusOK, nil, Request{})
}

func (h *Handler) renderRequest(w http.ResponseWriter, r *http.Request, status int, errs ValidationError, req Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	a := actor(r)
	p, err := h.svc.Bookable(r.Context(), a, id)
	if h.fail(w, r, err) {
		return
	}
	if open, err := h.svc.OpenFor(r.Context(), a.UserID, id); err == nil && open != nil {
		http.Redirect(w, r, "/viewings/"+open.ID.String(), http.StatusSeeOther)
		return
	}
	slots, err := h.svc.Slots(r.Context(), p)
	if h.fail(w, r, err) {
		return
	}
	v := pages.ViewingRequestView{
		ListingID: id.String(), Headline: headline(p), Area: areaOf(p), Cover: h.photoURL(id),
		Note: req.Note, FeeAck: req.FeeAck, Errors: errs, ListingURL: "/l/" + id.String(),
		HasHours: len(weekly.Normalize(p.Lister.ViewingHours)) > 0,
	}
	if fee := p.Fee(); fee != nil {
		v.Fee = fee.String()
	}
	if !req.StartsAt.IsZero() {
		v.Picked = req.StartsAt.UTC().Format(time.RFC3339)
	}
	var day *pages.SlotDay
	for _, t := range slots {
		label := t.In(Accra).Format("Mon 2 Jan")
		if day == nil || day.Label != label {
			v.Days = append(v.Days, pages.SlotDay{Label: label})
			day = &v.Days[len(v.Days)-1]
		}
		day.Slots = append(day.Slots, pages.Slot{Value: t.UTC().Format(time.RFC3339), Label: t.In(Accra).Format("15:04")})
	}
	now := h.svc.Now()
	v.MinDate = now.Add(MinNotice).In(Accra).Format(time.DateOnly)
	v.MaxDate = now.Add(Horizon).In(Accra).Format(time.DateOnly)
	for m := SuggestFrom; m+30 <= SuggestUntil; m += 30 {
		v.Times = append(v.Times, weekly.Clock(m))
	}
	render.Component(w, r, status, pages.ViewingRequest(layouts.Meta{Title: "Book a viewing", NoIndex: true}, v))
}

// Create books the request.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	req := Request{Note: r.PostFormValue("note"), FeeAck: r.PostFormValue("fee_ack") == "1"}
	req.StartsAt, err = parseWhen(r.PostFormValue("starts_at"), r.PostFormValue("date"), r.PostFormValue("time"))
	if err != nil {
		h.renderRequest(w, r, http.StatusUnprocessableEntity, ValidationError{"starts_at": "Pick a day and time."}, req)
		return
	}
	v, err := h.svc.Ask(r.Context(), actor(r), id, req)
	var verr ValidationError
	if errors.As(err, &verr) {
		h.renderRequest(w, r, http.StatusUnprocessableEntity, verr, req)
		return
	}
	if h.fail(w, r, err) {
		return
	}
	http.Redirect(w, r, "/viewings/"+v.ID.String()+"?sent=1", http.StatusSeeOther)
}

// parseWhen reads a slot (RFC 3339) or a suggested date + time (Accra).
func parseWhen(slot, date, clock string) (time.Time, error) {
	if slot != "" {
		return time.Parse(time.RFC3339, slot)
	}
	return time.ParseInLocation("2006-01-02 15:04", date+" "+clockPad(clock), Accra)
}

func clockPad(c string) string {
	if len(c) == 4 { // "9:00"
		return "0" + c
	}
	return c
}

// ── Viewing pages ────────────────────────────────────────────────────────

// Show is /viewings/{id}.
func (h *Handler) Show(w http.ResponseWriter, r *http.Request) {
	h.renderShow(w, r, http.StatusOK, nil)
}

func (h *Handler) renderShow(w http.ResponseWriter, r *http.Request, status int, errs ValidationError) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	a := actor(r)
	d, err := h.svc.Load(r.Context(), a, id)
	if h.fail(w, r, err) {
		return
	}
	h.svc.SeeLocation(r.Context(), a, d)
	v := h.detailView(d)
	if d.Role == "lister" {
		if rel, err := h.svc.ReliabilityOf(r.Context(), d.V.RenterID); err == nil && rel.Attended+rel.RenterMissed > 0 {
			v.RenterRecord = strconv.Itoa(rel.Attended) + " of " + strconv.Itoa(rel.Attended+rel.RenterMissed) + " past viewings attended"
		}
	}
	if d.Role == "renter" && !v.Upcoming && d.V.FeedbackAt == nil &&
		(d.V.Status == viewing.StatusConfirmed || d.V.Status == viewing.StatusCompleted || d.V.Status == viewing.StatusNoShow) {
		v.FeedbackURL = "/viewings/" + d.V.ID.String() + "/feedback"
	}
	v.Errors = errs
	if r.URL.Query().Get("sent") == "1" && d.V.Status == viewing.StatusRequested {
		v.Notice = "Request sent. We've texted the lister; you'll get an SMS when they reply."
	}
	w.Header().Set("Cache-Control", "private, no-store")
	render.Component(w, r, status, pages.ViewingDetail(layouts.Meta{Title: "Viewing · " + v.Headline, NoIndex: true}, v))
}

// Act handles the buttons: /viewings/{id}/{action}.
func (h *Handler) Act(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	act := Action(chi.URLParam(r, "action"))
	var ans Answer
	if act == Propose {
		if ans.StartsAt, err = parseWhen("", r.PostFormValue("date"), r.PostFormValue("time")); err != nil {
			h.renderShow(w, r, http.StatusUnprocessableEntity, ValidationError{"starts_at": "Pick a day and time to suggest."})
			return
		}
	}
	ans.Reason = r.PostFormValue("reason")
	_, err = h.svc.Act(r.Context(), actor(r), id, act, ans)
	var verr ValidationError
	if errors.As(err, &verr) {
		h.renderShow(w, r, http.StatusUnprocessableEntity, verr)
		return
	}
	if h.fail(w, r, err) {
		return
	}
	htmx.Redirect(w, r, "/viewings/"+id.String())
}

// Calendar is the .ics for a confirmed viewing.
func (h *Handler) Calendar(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	d, err := h.svc.Load(r.Context(), actor(r), id)
	if h.fail(w, r, err) {
		return
	}
	if !h.svc.Unlocked(d.V) {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="rentmap-viewing.ics"`)
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte(ICS(d, address(d), h.svc.baseURL+"/viewings/"+id.String(), h.svc.Now())))
}

// List is /viewings.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	mine, theirs, err := h.svc.Mine(r.Context(), a)
	if h.fail(w, r, err) {
		return
	}
	v := pages.ViewingListView{IsLister: reqctx.CurrentViewer(r.Context()).HasAny("landlord", "agent")}
	for _, d := range mine {
		v.Mine = append(v.Mine, h.rowView(d))
	}
	for _, d := range theirs {
		v.Theirs = append(v.Theirs, h.rowView(d))
	}
	if v.IsLister {
		hours, _ := h.svc.Hours(r.Context(), a.UserID)
		v.Hours = weekly.Describe(hours)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	render.Component(w, r, http.StatusOK, pages.ViewingList(layouts.Meta{Title: "Viewings", NoIndex: true}, v))
}

// HoursPage lets a lister set weekly viewing hours.
func (h *Handler) HoursPage(w http.ResponseWriter, r *http.Request) {
	hours, err := h.svc.Hours(r.Context(), actor(r).UserID)
	if h.fail(w, r, err) {
		return
	}
	render.Component(w, r, http.StatusOK, pages.ViewingHours(layouts.Meta{Title: "Viewing hours", NoIndex: true}, hoursView(hours, r.URL.Query().Get("saved") == "1")))
}

// SaveHours stores the form: day_N=1, from_N, to_N for N in 0..6.
func (h *Handler) SaveHours(w http.ResponseWriter, r *http.Request) {
	var ws []weekly.Window
	for d := range 7 {
		n := strconv.Itoa(d)
		if r.PostFormValue("day_"+n) != "1" {
			continue
		}
		from, ok1 := weekly.ParseClock(r.PostFormValue("from_" + n))
		to, ok2 := weekly.ParseClock(r.PostFormValue("to_" + n))
		if ok1 && ok2 {
			ws = append(ws, weekly.Window{Day: d, Start: from, End: to})
		}
	}
	if _, err := h.svc.SetHours(r.Context(), actor(r), ws); h.fail(w, r, err) {
		return
	}
	http.Redirect(w, r, "/viewings/hours?saved=1", http.StatusSeeOther)
}

// ── Views ────────────────────────────────────────────────────────────────

func headline(p *Place) string {
	if p.L.Headline != "" {
		return p.L.Headline
	}
	return "A place on RentMap"
}

func areaOf(p *Place) string {
	if n, ok := geo.NeighbourhoodBySlug(p.P.Neighbourhood); ok {
		return n.Label + ", " + p.P.City
	}
	return p.P.City
}

// address is the exact location in words (confirmed viewings only).
func address(d *Detail) string {
	var parts []string
	for _, s := range []string{d.Place.P.Street, d.Place.P.Landmark, areaOf(d.Place), d.Place.P.DigitalAddress} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

var statusLabels = map[viewing.Status]string{
	viewing.StatusRequested: "Waiting for reply", viewing.StatusProposed: "New time suggested", viewing.StatusConfirmed: "Confirmed",
	viewing.StatusDeclined: "Declined", viewing.StatusCancelled: "Cancelled", viewing.StatusCompleted: "Done", viewing.StatusNoShow: "Didn't happen",
}

func (h *Handler) rowView(d *Detail) pages.ViewingRow {
	other := d.Place.Lister
	if d.Role == "lister" {
		other = d.Renter
	}
	return pages.ViewingRow{URL: "/viewings/" + d.V.ID.String(), Headline: headline(d.Place), Area: areaOf(d.Place),
		When: When(d.V.StartsAt), Status: string(d.V.Status), StatusLabel: statusLabels[d.V.Status], With: nameOr(other),
		Cover: h.photoURL(d.V.ListingID), Past: d.V.StartsAt.Before(h.svc.Now()),
		NeedsYou: (d.Role == "lister" && d.V.Status == viewing.StatusRequested) || (d.Role == "renter" && d.V.Status == viewing.StatusProposed)}
}

func nameOr(u *ent.User) string {
	if u == nil || u.Name == "" {
		return "RentMap user"
	}
	return u.Name
}

// hoursView lays out the seven days with any saved window.
func hoursView(ws []weekly.Window, saved bool) pages.ViewingHoursView {
	v := pages.ViewingHoursView{Saved: saved}
	for m := 6 * 60; m <= 20*60; m += 30 {
		v.Times = append(v.Times, weekly.Clock(m))
	}
	for _, d := range []int{1, 2, 3, 4, 5, 6, 0} { // Monday first
		row := pages.HoursDay{Day: strconv.Itoa(d), Name: weekly.DayNames[d], From: "9:00", To: "13:00"}
		for _, w := range ws {
			if w.Day == d {
				row.On, row.From, row.To = true, weekly.Clock(w.Start), weekly.Clock(w.End)
				break
			}
		}
		v.Days = append(v.Days, row)
	}
	return v
}

func (h *Handler) detailView(d *Detail) pages.ViewingDetailView {
	v, now := d.V, h.svc.Now()
	dv := pages.ViewingDetailView{
		ID: v.ID.String(), Role: d.Role, Status: string(v.Status), StatusLabel: statusLabels[v.Status],
		Headline: headline(d.Place), Area: areaOf(d.Place), ListingURL: "/l/" + v.ListingID.String(), Cover: h.photoURL(v.ListingID),
		When: When(v.StartsAt), Duration: strconv.Itoa(v.DurationMin) + " min", Note: v.Note,
		Upcoming: v.StartsAt.After(now), Unlocked: h.svc.Unlocked(v),
		RenterName: nameOr(d.Renter), ListerName: nameOr(d.Place.Lister), DeclineReason: ReasonLabel(v.DeclineReason),
	}
	if v.ViewingFee != nil {
		dv.Fee = v.ViewingFee.String()
	}
	for _, r := range DeclineReasons {
		dv.Reasons = append(dv.Reasons, pages.Opt{Value: r.Key, Label: r.Label})
	}
	dv.MinDate = now.Add(MinNotice).In(Accra).Format(time.DateOnly)
	dv.MaxDate = now.Add(Horizon).In(Accra).Format(time.DateOnly)
	for m := SuggestFrom; m+30 <= SuggestUntil; m += 30 {
		dv.Times = append(dv.Times, weekly.Clock(m))
	}
	if dv.Unlocked {
		p := d.Place.P
		dv.Address = address(d)
		if p.Lat != nil && p.Lng != nil {
			ll := strconv.FormatFloat(*p.Lat, 'f', 6, 64) + "," + strconv.FormatFloat(*p.Lng, 'f', 6, 64)
			dv.Coords = ll
			dv.GoogleURL = "https://www.google.com/maps/dir/?api=1&destination=" + ll
			dv.WazeURL = "https://waze.com/ul?ll=" + ll + "&navigate=yes"
		}
		other := d.Place.Lister
		if d.Role == "lister" {
			other = d.Renter
		}
		if other.Phone != nil {
			dv.OtherPhone = phone.Pretty(*other.Phone)
			dv.OtherTel = "tel:" + *other.Phone
			dv.OtherWhatsApp = "https://wa.me/" + strings.TrimPrefix(*other.Phone, "+")
		}
	}
	return dv
}

// ── Feedback ─────────────────────────────────────────────────────────────

// FeedbackPage asks the renter how the viewing went.
func (h *Handler) FeedbackPage(w http.ResponseWriter, r *http.Request) {
	h.renderFeedback(w, r, http.StatusOK, nil, false)
}

func (h *Handler) renderFeedback(w http.ResponseWriter, r *http.Request, status int, errs ValidationError, done bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	d, err := h.svc.Load(r.Context(), actor(r), id)
	if h.fail(w, r, err) {
		return
	}
	if d.Role != "renter" {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	v := pages.FeedbackView{ViewingID: id.String(), Headline: headline(d.Place), When: When(d.V.StartsAt),
		Lister: firstName(d.Place.Lister), Done: done || d.V.FeedbackAt != nil, Errors: errs}
	if v.Lister == "A renter" {
		v.Lister = "the lister"
	}
	render.Component(w, r, status, pages.Feedback(layouts.Meta{Title: "How was the viewing?", NoIndex: true}, v))
}

// SaveFeedback stores the renter's answers.
func (h *Handler) SaveFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		render.Error(w, r, http.StatusNotFound)
		return
	}
	f := Feedback{Outcome: r.PostFormValue("outcome"), Accuracy: r.PostFormValue("accuracy"), Note: r.PostFormValue("note")}
	switch r.PostFormValue("interested") {
	case "yes":
		t := true
		f.Interested = &t
	case "no":
		no := false
		f.Interested = &no
	}
	_, err = h.svc.GiveFeedback(r.Context(), actor(r), id, f)
	var verr ValidationError
	if errors.As(err, &verr) {
		h.renderFeedback(w, r, http.StatusUnprocessableEntity, verr, false)
		return
	}
	if h.fail(w, r, err) {
		return
	}
	h.renderFeedback(w, r, http.StatusOK, nil, true)
}
