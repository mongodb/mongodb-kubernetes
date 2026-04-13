package status

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MigrationLifecyclePhase describes the overall VM-to-K8s migration lifecycle stage.
type MigrationLifecyclePhase string

const (
	// MigrationPhaseValidating is set while the migration-dry-run annotation is present.
	MigrationPhaseValidating MigrationLifecyclePhase = "Validating"
	// MigrationPhaseExtending is set when the running in-cluster member count (status.members) increased
	// since the previous reconcile. The replica set is being extended with new k8s members.
	MigrationPhaseExtending MigrationLifecyclePhase = "Extending"
	// MigrationPhasePruning is set when spec.externalMembers count decreases — VM members are being removed.
	MigrationPhasePruning MigrationLifecyclePhase = "Pruning"
	// MigrationPhaseInProgress is the stable state: externalMembers exist, counts are unchanged,
	// no dry-run active. Migration is active but nothing is changing right now.
	MigrationPhaseInProgress MigrationLifecyclePhase = "InProgress"
)

// MigrationStatus captures the state of the VM-to-K8s migration process.
// +kubebuilder:object:generate=true
type MigrationStatus struct {
	// Phase is the current stage of the migration lifecycle.
	Phase MigrationLifecyclePhase `json:"phase,omitempty"`

	// ObservedExternalMembersCount is the number of externalMembers seen on the previous reconcile.
	// Used to detect Pruning (count decreased).
	ObservedExternalMembersCount int `json:"observedExternalMembersCount,omitempty"`

	// Conditions contains individual check results (e.g. NetworkConnectivityVerification).
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ConditionNetworkConnectivityVerification Condition type for migration (connectivity validation) dry run.
const ConditionNetworkConnectivityVerification = "NetworkConnectivityVerification"

// MigrationPhase describes the current phase of a connectivity validation dry run.
type MigrationPhase string

const (
	MigrationPhaseConnectivityCheckRunning MigrationPhase = "ConnectivityCheckRunning"
	MigrationPhaseConnectivityCheckPassed  MigrationPhase = "ConnectivityCheckPassed"
	MigrationPhaseConnectivityCheckFailed  MigrationPhase = "ConnectivityCheckFailed"
)

// MigrationCondition returns a metav1.Condition for the migration connectivity check.
// Passed -> True, Failed -> False, Running -> Unknown.
//
// Unknown when running follows standard Kubernetes condition semantics:
//   - True = condition satisfied,
//   - False = not satisfied,
//   - Unknown = outcome not yet known (verification in progress).
func MigrationCondition(phase MigrationPhase, reason, message string) metav1.Condition {
	status := metav1.ConditionUnknown
	switch phase {
	case MigrationPhaseConnectivityCheckPassed:
		status = metav1.ConditionTrue
	case MigrationPhaseConnectivityCheckFailed:
		status = metav1.ConditionFalse
	}
	return metav1.Condition{
		Type:               ConditionNetworkConnectivityVerification,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}
