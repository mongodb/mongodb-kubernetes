package migration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNetworkConditionFromExitCode_Reasons(t *testing.T) {
	cases := map[int32]string{
		ExitSuccess:           "NetworkValidationPassed",
		ExitAuthFailed:        "AuthenticationFailed",
		ExitRoleNotFound:      "SystemRoleNotFound",
		ExitMemberUnreachable: "MemberUnreachable",
		ExitDNSFailed:         "DNSResolutionFailed",
		ExitTLSFailed:         "TLSHandshakeFailed",
		99:                    "UnknownError",
	}
	for code, wantReason := range cases {
		_, reason, _ := NetworkConditionFromExitCode(code)
		assert.Equal(t, wantReason, reason, "exit code %d", code)
	}
}

func TestNetworkConditionFromExitCode_ConditionStatus(t *testing.T) {
	condStatus, _, _ := NetworkConditionFromExitCode(ExitSuccess)
	assert.Equal(t, metav1.ConditionTrue, condStatus)

	for _, code := range []int32{ExitAuthFailed, ExitRoleNotFound, ExitMemberUnreachable, ExitDNSFailed, ExitTLSFailed, ExitUnknown, 99} {
		condStatus, _, _ := NetworkConditionFromExitCode(code)
		assert.Equal(t, metav1.ConditionFalse, condStatus, "exit code %d", code)
	}
}

func TestNetworkConditionFromExitCode_UnknownMessage(t *testing.T) {
	_, _, msg := NetworkConditionFromExitCode(99)
	assert.Contains(t, msg, "99")
}
