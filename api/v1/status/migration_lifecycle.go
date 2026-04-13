package status

// MigrationDryRunAnnotationKey is the annotation key that triggers migration connectivity dry-run.
const MigrationDryRunAnnotationKey = "mongodb.com/migration-dry-run"

// ComputeMigrationLifecyclePhase derives migration.phase from inputs (pure).
//
// The caller is responsible for clearing status.migration when externalCount == 0.
//
// Phases (in priority order):
//   - Validating  — migration-dry-run annotation is present
//   - Pruning     — external member count decreased since last reconcile (takes precedence over Extending)
//   - Extending   — spec.members exceeds the last-reconciled K8s member count (members being provisioned)
//   - InProgress  — stable (counts unchanged, no dry-run)
func ComputeMigrationLifecyclePhase(isDryRun bool, externalCount int, prevObservedExternalCount int, desiredK8sMembers int, lastReconciledK8sMembers int) MigrationLifecyclePhase {
	if isDryRun {
		return MigrationPhaseValidating
	}

	if externalCount < prevObservedExternalCount {
		return MigrationPhasePruning
	}

	if desiredK8sMembers > lastReconciledK8sMembers {
		return MigrationPhaseExtending
	}

	return MigrationPhaseInProgress
}
