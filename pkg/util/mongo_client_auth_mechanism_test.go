package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// SCRAM umbrella resolution depends on OM autoAuthMechanism and authEnabled (see WireAuthMechanismForCRAuthMode godoc).
func TestWireAuthMechanismForCRAuthMode_SCRAMUmbrella(t *testing.T) {
	assert.Equal(t, AutomationConfigScramSha1Option,
		WireAuthMechanismForCRAuthMode(SCRAM, AutomationConfigScramSha1Option, true))
	assert.Equal(t, AutomationConfigScramSha256Option,
		WireAuthMechanismForCRAuthMode(SCRAM, AutomationConfigScramSha1Option, false))
	assert.Equal(t, AutomationConfigScramSha256Option,
		WireAuthMechanismForCRAuthMode(SCRAM, AutomationConfigScramSha256Option, true))
}

func TestWireAuthMechanismForCRAuthMode_unknownReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", WireAuthMechanismForCRAuthMode("", "", false))
	assert.Equal(t, "", WireAuthMechanismForCRAuthMode("not-a-valid-mode", "", false))
}
