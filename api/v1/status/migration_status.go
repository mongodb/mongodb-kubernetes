package status

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition type for migration (connectivity validation) dry run.
const ConditionNetworkConnectivityVerified = "NetworkConnectivityVerified"

// MigrationPhase describes the current phase of a connectivity validation dry run.
type MigrationPhase string

const (
	MigrationPhaseConnectivityCheckRunning MigrationPhase = "ConnectivityCheckRunning"
	MigrationPhaseConnectivityCheckPassed  MigrationPhase = "ConnectivityCheckPassed"
	MigrationPhaseConnectivityCheckFailed  MigrationPhase = "ConnectivityCheckFailed"
)

// MigrationCondition returns a metav1.Condition for the migration connectivity check.
// Passed -> True, Failed -> False, Running -> Unknown.
func MigrationCondition(phase MigrationPhase, reason, message string) metav1.Condition {
	status := metav1.ConditionUnknown
	switch phase {
	case MigrationPhaseConnectivityCheckPassed:
		status = metav1.ConditionTrue
	case MigrationPhaseConnectivityCheckFailed:
		status = metav1.ConditionFalse
	}
	return metav1.Condition{
		Type:               ConditionNetworkConnectivityVerified,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}
