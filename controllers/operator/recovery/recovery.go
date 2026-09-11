package recovery

import (
	"time"

	"go.uber.org/zap"
)

// ShouldTriggerRecovery reports whether the operator should force-push the automation config to
// recover a resource with a broken automation config. Automatic recovery is enabled and its
// back-off (in seconds) are sourced from the OperatorConfig (.spec.automaticRecovery) and threaded
// in by the caller.
func ShouldTriggerRecovery(enabled bool, backoffSeconds int, isResourceFailing bool, lastTransitionTime string) bool {
	if enabled && isResourceFailing {
		parsedTime, err := time.Parse(time.RFC3339, lastTransitionTime)
		if err != nil {
			// We silently ignore all the errors and just prevent the recovery from happening
			return false
		}
		zap.S().Debugf("The configured delay before recovery is %d seconds", backoffSeconds)
		if parsedTime.Add(time.Duration(backoffSeconds) * time.Second).Before(time.Now()) {
			return true
		}
	}
	return false
}
