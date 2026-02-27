package connectivitycheck

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ExitSuccess           = 0
	ExitUnknown           = 1 // unclassified; reserved so set -e traps don't collide
	ExitAuthFailed        = 2 // credentials rejected
	ExitRoleNotFound      = 3 // auth ok, but __system@local role absent
	ExitMemberUnreachable = 4 // ping timed out / connection refused
	ExitDNSFailed         = 5 // hostname resolution failed
	ExitTLSFailed         = 6 // TLS handshake error

	// ConditionNetworkConnectivityVerified is the condition type written to status.migration.conditions.
	ConditionNetworkConnectivityVerified = "NetworkConnectivityVerified"
)

// NetworkConditionFromExitCode returns the status, reason, and message needed to populate a metav1.Condition
// of type ConditionNetworkConnectivityVerified.
func NetworkConditionFromExitCode(code int32) (conditionStatus metav1.ConditionStatus, reason, message string) {
	s := metav1.ConditionTrue
	if code != ExitSuccess {
		s = metav1.ConditionFalse
	}
	switch int(code) {
	case ExitSuccess:
		return s, "NetworkValidationPassed", "All external members reachable and authenticated"
	case ExitAuthFailed:
		return s, "AuthenticationFailed", "Cluster credentials rejected by external members"
	case ExitRoleNotFound:
		return s, "SystemRoleNotFound", "Authenticated but __system@local role not present; check keyfile/cert"
	case ExitMemberUnreachable:
		return s, "MemberUnreachable", "One or more external members did not respond to ping"
	case ExitDNSFailed:
		return s, "DNSResolutionFailed", "Could not resolve one or more external member hostnames"
	case ExitTLSFailed:
		return s, "TLSHandshakeFailed", "TLS handshake failed — verify CA and certificate validity"
	default:
		return s, "UnknownError", fmt.Sprintf("Validation job exited with unexpected code %d", code)
	}
}
