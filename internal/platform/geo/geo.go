// Package geo holds location maths and privacy rules (ProjectRequirement §6.1).
//
// The exact point of a property is private. Everything public — map pins,
// listing pages, distances, search filters — uses the approximate point: a
// fixed 150–400 m offset derived from HMAC(secret, property id). Because the
// offset is deterministic, repeated requests can't be averaged to find the
// real spot.
package geo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// Point is a WGS84 coordinate.
type Point struct{ Lat, Lng float64 }

const (
	earthRadius  = 6371008.8 // metres
	minOffset    = 150.0
	maxOffset    = 400.0
	recomputeMin = 50.0 // exact point must move this far before the approximate one changes
)

// Approximate returns the public point for a property's exact point.
func Approximate(secret []byte, propertyID uuid.UUID, exact Point) Point {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("approx:v1:"))
	m.Write(propertyID[:])
	h := m.Sum(nil)

	angle := float64(binary.BigEndian.Uint64(h[0:8])) / math.MaxUint64 * 2 * math.Pi
	frac := float64(binary.BigEndian.Uint64(h[8:16])) / math.MaxUint64
	dist := minOffset + frac*(maxOffset-minOffset)
	return round5(Offset(exact, dist, angle))
}

// ShouldRecompute reports whether an edit moved the exact point far enough
// to move the approximate one. Small nudges keep the old approximate point,
// so edits can't be used to triangulate.
func ShouldRecompute(oldExact, newExact Point) bool {
	return Distance(oldExact, newExact) > recomputeMin
}

// Offset moves p by dist metres on a bearing (radians, clockwise from north).
func Offset(p Point, dist, bearing float64) Point {
	dLat := dist * math.Cos(bearing) / earthRadius
	dLng := dist * math.Sin(bearing) / (earthRadius * math.Cos(p.Lat*math.Pi/180))
	return Point{Lat: p.Lat + dLat*180/math.Pi, Lng: p.Lng + dLng*180/math.Pi}
}

// Distance is the great-circle distance in metres.
func Distance(a, b Point) float64 {
	la1, la2 := a.Lat*math.Pi/180, b.Lat*math.Pi/180
	dLat := la2 - la1
	dLng := (b.Lng - a.Lng) * math.Pi / 180
	s := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadius * math.Asin(math.Min(1, math.Sqrt(s)))
}

// PublicDistance formats a distance computed from an approximate point:
// 100 m steps under 1 km, 0.5 km steps above (§6.1 rule 6).
func PublicDistance(metres float64) string {
	if metres < 1000 {
		m := math.Max(100, math.Round(metres/100)*100)
		if m >= 1000 {
			return "~1 km"
		}
		return fmt.Sprintf("~%d m", int(m))
	}
	km := math.Round(metres/500) * 0.5
	if km == math.Trunc(km) {
		return fmt.Sprintf("~%d km", int(km))
	}
	return fmt.Sprintf("~%.1f km", km)
}

// InGhana is a coarse bounding-box check (catches swapped lat/lng and 0,0).
func InGhana(p Point) bool {
	return p.Lat >= 4.5 && p.Lat <= 11.3 && p.Lng >= -3.4 && p.Lng <= 1.3
}

var digitalAddress = regexp.MustCompile(`^([A-Z]{2})-?([0-9]{3,4})-?([0-9]{4})$`)

// NormalizeDigitalAddress formats a GhanaPostGPS address ("ak0395028",
// "AK 039 5028") as "AK-039-5028", or returns "" if it isn't one.
func NormalizeDigitalAddress(raw string) string {
	s := strings.ToUpper(strings.Join(strings.Fields(raw), ""))
	m := digitalAddress.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1] + "-" + m[2] + "-" + m[3]
}

func round5(p Point) Point {
	return Point{Lat: math.Round(p.Lat*1e5) / 1e5, Lng: math.Round(p.Lng*1e5) / 1e5}
}
