// Package exitcode defines numeric exit codes for the connectivity-validator binary.
// It has no Kubernetes dependencies so the validator links only this package, not exitcondition.
package exitcode

import "fmt"

const (
	ExitSuccess       = 0
	ExitUnknown       = 1 // unclassified
	ExitAuthFailed    = 2 // credentials rejected or __system@local role missing
	ExitNetworkFailed = 3 // DNS, TLS, timeouts, unreachable members
)

// Name returns a short name for the exit code for logging.
func Name(code int) string {
	switch code {
	case ExitSuccess:
		return "Success"
	case ExitUnknown:
		return "Unknown"
	case ExitAuthFailed:
		return "AuthFailed"
	case ExitNetworkFailed:
		return "NetworkFailed"
	default:
		return fmt.Sprintf("Exit%d", code)
	}
}
