package recovery

import (
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

const (
	EnableRecoveryEnvVar                       = "MDB_AUTOMATIC_RECOVERY_ENABLE"
	RecoveryBackoffTimeEnvVar                  = "MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S"
	DefaultAutomaticRecoveryBackoffTimeSeconds = 20 * 60
)

func isAutomaticRecoveryTurnedOn() bool {
	return env.ReadBoolOrDefault(EnableRecoveryEnvVar, true)
}

func automaticRecoveryBackoffSeconds() int {
	return env.ReadIntOrDefault(RecoveryBackoffTimeEnvVar, DefaultAutomaticRecoveryBackoffTimeSeconds)
}

func ShouldTriggerRecovery(isResourceFailing bool, lastTransitionTime string) bool {
	if isAutomaticRecoveryTurnedOn() && isResourceFailing {
		parsedTime, err := time.Parse(time.RFC3339, lastTransitionTime)
		if err != nil {
			// We silently ignore all the errors and just prevent the recovery from happening
			return false
		}
		if parsedTime.Add(time.Duration(automaticRecoveryBackoffSeconds()) * time.Second).Before(time.Now()) {
			return true
		}
	}
	return false
}
