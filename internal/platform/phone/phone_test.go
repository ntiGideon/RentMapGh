package phone

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeGhana(t *testing.T) {
	ok := map[string]string{
		"0241234567":         "+233241234567",
		"024 123 4567":       "+233241234567",
		"024-123-4567":       "+233241234567",
		"241234567":          "+233241234567", // typed after the +233 prefix
		"+233 24 123 4567":   "+233241234567",
		"00233241234567":     "+233241234567",
		"(050) 123 4567":     "+233501234567",
		" 0201234567 ":       "+233201234567",
		"+233 (0)55 1234567": "+233551234567",
	}
	for in, want := range ok {
		got, err := NormalizeGhana(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	bad := map[string]error{
		"":               ErrEmpty,
		"12345":          ErrInvalid,
		"+2348031234567": ErrInvalid, // Nigerian number
		"+14155552671":   ErrInvalid,
		"0302123456":     ErrNotMobile, // Accra landline
		"hello":          ErrInvalid,
	}
	for in, want := range bad {
		_, err := NormalizeGhana(in)
		assert.ErrorIs(t, err, want, in)
	}
}

func TestMaskAndPretty(t *testing.T) {
	assert.Equal(t, "+233 24 *** 4567", Mask("+233241234567"))
	assert.Equal(t, "+14155552671", Mask("+14155552671"))
	assert.Equal(t, "024 123 4567", Pretty("+233241234567"))
}
