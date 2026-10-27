// Package connectivityexit provides the operator-side view of connectivity-validator exit codes.
// The single source of truth for the numeric values is
// cmd/connectivity-validator/exitcode/exitcode.go (validator module). This package re-exports
// those constants and adds the Kubernetes condition mapping so the operator has one import point
// for everything related to validator exit codes.
package connectivityexit

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/exitcode"
)

// NetworkConditionFromExitCode returns the status, reason, and message needed to populate a metav1.Condition
// of type ConditionNetworkConnectivityVerified.
func NetworkConditionFromExitCode(code int32) (conditionStatus metav1.ConditionStatus, reason, message string) {
	s := metav1.ConditionTrue
	if code != exitcode.ExitSuccess {
		s = metav1.ConditionFalse
	}
	switch code {
	case exitcode.ExitSuccess:
		return s, "NetworkValidationPassed", "All external members reachable and authenticated"
	case exitcode.ExitAuthFailed:
		return s, "AuthenticationFailed", "Authentication failed — check credentials, auth mechanism, and __system@local role in Ops Manager"
	case exitcode.ExitNetworkFailed:
		return s, "NetworkFailed", "Network connectivity failed — check DNS, TLS, firewalls, and member addresses (see Job pod logs)"
	default:
		return s, "UnknownError", fmt.Sprintf("Validation job exited with unexpected code %d", code)
	}
}
