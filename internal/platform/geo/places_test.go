package geo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func slugs(ps []Place) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Slug)
	}
	return out
}

func TestSearchPlaces(t *testing.T) {
	cases := []struct {
		q     string
		first string
	}{
		{"knust", "knust"},
		{"KNUST", "knust"},
		{"kwame nkrumah", "knust"},
		{"tech", "knust"}, // alias prefix, campus ranks first
		{"tech junc", "tech-junction"},
		{"kath", "kath"},
		{"komfo", "kath"},
		{"kejetia", "kejetia"},
		{"central market", "kejetia"},
		{"Kótei", "kotei"}, // accents folded
		{"ayed", "ayeduase"},
		{"poly", "kstu"}, // word inside an alias
		{"  bomso  ", "bomso"},
	}
	for _, tc := range cases {
		got := SearchPlaces(tc.q, 5)
		require.NotEmpty(t, got, tc.q)
		assert.Equal(t, tc.first, got[0].Slug, "%q → %v", tc.q, slugs(got))
	}
	assert.Empty(t, SearchPlaces("", 5))
	assert.Empty(t, SearchPlaces("zzzz", 5))
	assert.Len(t, SearchPlaces("a", 3), 3, "capped")
}

func TestPlacesAreUniqueAndInKumasi(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Places {
		assert.False(t, seen[p.Slug], "duplicate slug %s", p.Slug)
		seen[p.Slug] = true
		assert.Less(t, Distance(p.Point, KNUST), 25_000.0, "%s is more than 25 km from KNUST", p.Slug)
		_, ok := PlaceBySlug(p.Slug)
		assert.True(t, ok)
	}
}

func TestNearbyPlaces(t *testing.T) {
	near := NearbyPlaces(Point{6.6700, -1.5600}, 3000, 3)
	require.NotEmpty(t, near)
	assert.Equal(t, "knust", near[0].Place.Slug)
	for i := 1; i < len(near); i++ {
		assert.LessOrEqual(t, near[i-1].Metres, near[i].Metres)
	}
	for _, n := range near {
		assert.NotEqual(t, KindArea, n.Place.Kind, "neighbourhoods aren't 'nearby places'")
	}
}
