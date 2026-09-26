package geo

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

var knust = Point{Lat: 6.6745, Lng: -1.5716}

func TestApproximateIsDeterministicAndInRange(t *testing.T) {
	secret := []byte("secret")
	for i := 0; i < 500; i++ {
		id := uuid.New()
		a := Approximate(secret, id, knust)
		assert.Equal(t, a, Approximate(secret, id, knust), "same input, same point")
		d := Distance(knust, a)
		assert.GreaterOrEqual(t, d, minOffset-2, "offset too small: %.1f m", d) // 5-decimal rounding ≈ ±1 m
		assert.LessOrEqual(t, d, maxOffset+2, "offset too large: %.1f m", d)
	}
}

func TestApproximateDependsOnSecretAndID(t *testing.T) {
	id := uuid.New()
	a := Approximate([]byte("one"), id, knust)
	assert.NotEqual(t, a, Approximate([]byte("two"), id, knust))
	assert.NotEqual(t, a, Approximate([]byte("one"), uuid.New(), knust))
}

func TestApproximateDirectionsAreSpread(t *testing.T) {
	// Every quadrant should appear: no systematic bias an attacker could undo.
	quadrants := map[[2]bool]int{}
	for i := 0; i < 400; i++ {
		a := Approximate([]byte("s"), uuid.New(), knust)
		quadrants[[2]bool{a.Lat > knust.Lat, a.Lng > knust.Lng}]++
	}
	assert.Len(t, quadrants, 4)
	for q, n := range quadrants {
		assert.Greater(t, n, 50, "quadrant %v under-represented", q)
	}
}

func TestShouldRecompute(t *testing.T) {
	assert.False(t, ShouldRecompute(knust, Offset(knust, 30, 1)))
	assert.True(t, ShouldRecompute(knust, Offset(knust, 80, 1)))
}

func TestPublicDistance(t *testing.T) {
	for m, want := range map[float64]string{
		20: "~100 m", 149: "~100 m", 151: "~200 m", 940: "~900 m", 980: "~1 km",
		1200: "~1 km", 1300: "~1.5 km", 2240: "~2 km", 2260: "~2.5 km",
	} {
		assert.Equal(t, want, PublicDistance(m), m)
	}
}

func TestDigitalAddress(t *testing.T) {
	for in, want := range map[string]string{
		"AK-039-5028": "AK-039-5028", "ak0395028": "AK-039-5028", "ak 039 5028": "AK-039-5028",
		"GA-1838-164": "", "GA18381640": "GA-1838-1640", "123": "", "": "",
	} {
		assert.Equal(t, want, NormalizeDigitalAddress(in), in)
	}
}

func TestInGhana(t *testing.T) {
	assert.True(t, InGhana(knust))
	assert.False(t, InGhana(Point{Lat: -1.5716, Lng: 6.6745}), "swapped")
	assert.False(t, InGhana(Point{}))
}
