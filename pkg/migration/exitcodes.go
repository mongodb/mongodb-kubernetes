package migration

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ExitSuccess       = 0
	ExitUnknown       = 1 // unclassified
	ExitAuthFailed    = 2 // credentials rejected or __system@local role missing
	ExitNetworkFailed = 3 // DNS, TLS, timeouts, unreachable members
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
		return s, "AuthenticationFailed", "Authentication failed — check credentials, auth mechanism, and __system@local role in Ops Manager"
	case ExitNetworkFailed:
		return s, "NetworkFailed", "Network connectivity failed — check DNS, TLS, firewalls, and member addresses (see Job pod logs)"
	default:
		return s, "UnknownError", fmt.Sprintf("Validation job exited with unexpected code %d", code)
	}
}
