package viewings

import (
	"strings"
	"time"
)

// ICS is a one-event calendar file for a confirmed viewing (RFC 5545), with
// a reminder two hours before. where is shown only to confirmed parties.
func ICS(d *Detail, where, url string, stamp time.Time) string {
	v := d.V
	end := v.StartsAt.Add(time.Duration(v.DurationMin) * time.Minute)
	f := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	title := "Viewing"
	if d.Place.L.Headline != "" {
		title += ": " + d.Place.L.Headline
	}
	lines := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//RentMap Ghana//Viewings//EN",
		"CALSCALE:GREGORIAN",
		"METHOD:PUBLISH",
		"BEGIN:VEVENT",
		"UID:" + v.ID.String() + "@rentmap.gh",
		"DTSTAMP:" + f(stamp),
		"DTSTART:" + f(v.StartsAt),
		"DTEND:" + f(end),
		"SUMMARY:" + icsText(title),
		"LOCATION:" + icsText(where),
		"DESCRIPTION:" + icsText("Details, directions and phone numbers: "+url+"\nNever pay rent or fees before you've seen the place and met the owner or agent."),
		"URL:" + url,
		"STATUS:CONFIRMED",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"DESCRIPTION:" + icsText(title),
		"TRIGGER:-PT2H",
		"END:VALARM",
		"END:VEVENT",
		"END:VCALENDAR",
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(fold(l))
		b.WriteString("\r\n")
	}
	return b.String()
}

// icsText escapes a TEXT value.
func icsText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`)
	return r.Replace(s)
}

// fold wraps content lines at 75 octets, continuing with a space.
func fold(l string) string {
	if len(l) <= 75 {
		return l
	}
	var b strings.Builder
	n := 0
	for _, r := range l {
		w := len(string(r))
		if n+w > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += w
	}
	return b.String()
}
