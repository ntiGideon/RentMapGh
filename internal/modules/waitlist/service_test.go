package waitlist

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	in, phone, errs := validate(Input{Name: "  Ama   Serwaa ", Phone: "0241234567", Area: "bomso", Consent: true})
	require.Nil(t, errs)
	assert.Equal(t, "+233241234567", phone)
	assert.Equal(t, "Ama Serwaa", in.Name)
	assert.Equal(t, "renter", in.Role, "empty role defaults to renter")

	_, _, errs = validate(Input{Phone: "1", Role: "admin", Area: "lagos"})
	require.NotNil(t, errs)
	assert.Contains(t, errs, "phone")
	assert.Contains(t, errs, "form")
	assert.Contains(t, errs, "area")
	assert.Contains(t, errs, "consent")
}
