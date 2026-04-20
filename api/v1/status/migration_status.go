package status

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MigratingConditionReason is the string written to status.conditions[Migrating].reason while
// VM-to-K8s migration is active (Migrating status True).
type MigratingConditionReason string

const (
	// MigratingReasonValidating is used while the migration-dry-run annotation is present.
	MigratingReasonValidating MigratingConditionReason = "Validating"
	// MigratingReasonExtending is used when spec.members exceeds the last-reconciled in-cluster member count
	// (status.members from the prior reconcile). The replica set is being extended with new k8s members.
	MigratingReasonExtending MigratingConditionReason = "Extending"
	// MigratingReasonPruning is used when spec.externalMembers count decreases — VM members are being removed.
	MigratingReasonPruning MigratingConditionReason = "Pruning"
	// MigratingReasonInProgress is the stable state: externalMembers exist, counts are unchanged,
	// no dry-run active. Migration is active but nothing is changing right now.
	MigratingReasonInProgress MigratingConditionReason = "InProgress"
	// MigratingReasonComplete is set on the Migrating=False condition after all external members are removed.
	MigratingReasonComplete MigratingConditionReason = "MigrationComplete"
)

// ConditionNetworkConnectivityVerification Condition type for migration (connectivity validation) dry run.
const ConditionNetworkConnectivityVerification = "NetworkConnectivityVerification"

// LegacyMigrationObservedExternalMembersConditionType is the former condition type for the observed
// external-member count. Stripped on reconcile for CRs upgraded from older operators.
const LegacyMigrationObservedExternalMembersConditionType = "MigrationObservedExternalMembers"

// ConditionMigrating is the top-level condition type indicating whether VM-to-K8s migration is active.
// True = migration in progress (externalMembers exist), False = migration complete or not started.
const ConditionMigrating = "Migrating"

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

// MigratingCondition returns a top-level Migrating condition for the MongoDB resource.
// This provides kubectl wait compatibility: kubectl wait --for=condition=Migrating=False
func MigratingCondition(migrating bool, reason MigratingConditionReason) metav1.Condition {
	if migrating {
		return metav1.Condition{
			Type:               ConditionMigrating,
			Status:             metav1.ConditionTrue,
			Reason:             string(reason),
			Message:            "VM-to-Kubernetes migration is in progress",
			LastTransitionTime: metav1.Now(),
		}
	}
	return metav1.Condition{
		Type:               ConditionMigrating,
		Status:             metav1.ConditionFalse,
		Reason:             string(MigratingReasonComplete),
		Message:            "VM-to-Kubernetes migration finished: all external members removed",
		LastTransitionTime: metav1.Now(),
	}
}
