package status

// MigrationDryRunAnnotationKey is the annotation key that triggers migration connectivity dry-run.
const MigrationDryRunAnnotationKey = "mongodb.com/migration-dry-run"

// ComputeMigratingConditionReason derives the Migrating condition reason from inputs (pure).
//
// The caller is responsible for clearing migration-related status (conditions and
// migrationObservedExternalMembersCount) when externalCount == 0.
//
// Reasons (in priority order) match status.conditions[Migrating].reason while migrating:
//   - Validating  — migration-dry-run annotation is present
//   - Pruning     — external member count decreased since last reconcile (takes precedence over Extending)
//   - Extending   — spec.members exceeds the last-reconciled K8s member count (members being provisioned)
//   - InProgress  — stable (counts unchanged, no dry-run)
func ComputeMigratingConditionReason(isDryRun bool, externalCount int, prevObservedExternalCount int, desiredK8sMembers int, lastReconciledK8sMembers int) MigratingConditionReason {
	if isDryRun {
		return MigratingReasonValidating
	}

	if externalCount < prevObservedExternalCount {
		return MigratingReasonPruning
	}

	if desiredK8sMembers > lastReconciledK8sMembers {
		return MigratingReasonExtending
	}

	return MigratingReasonInProgress
}
