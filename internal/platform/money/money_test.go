package money

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]Pesewas{
		"1200": 120000, "1,200": 120000, "GH₵ 1,200.50": 120050, "₵850": 85000,
		"850.5": 85050, "0": 0, ".50": 50, "ghs 45": 4500, " 7 200 ": 720000,
	} {
		got, err := Parse(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "abc", "12.345", "-5", "1.", "100000000"} {
		_, err := Parse(bad)
		assert.ErrorIs(t, err, ErrInvalid, bad)
	}
}

func TestFormat(t *testing.T) {
	assert.Equal(t, "GH₵ 1,200", Pesewas(120000).String())
	assert.Equal(t, "GH₵ 850.50", Pesewas(85050).String())
	assert.Equal(t, "GH₵ 0", Pesewas(0).String())
	assert.Equal(t, "1,234,567.05", Pesewas(123456705).Number())
	assert.Equal(t, "1200", Pesewas(120000).Input())
	assert.Equal(t, "850.50", Pesewas(85050).Input())
}
