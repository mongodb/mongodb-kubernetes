package status

// MigrationPhase describes the current phase of a connectivity validation dry run.
type MigrationPhase string

const (
	// MigrationPhaseConnectivityCheckRunning means the connectivity validation Job
	// has been created and is still in progress.
	MigrationPhaseConnectivityCheckRunning MigrationPhase = "ConnectivityCheckRunning"

	// MigrationPhaseConnectivityCheckPassed means the Job completed successfully:
	// all external members were reachable and authenticated.
	MigrationPhaseConnectivityCheckPassed MigrationPhase = "ConnectivityCheckPassed"

	// MigrationPhaseConnectivityCheckFailed means the Job completed with a non-zero
	// exit code; see status.migration.message for the reason.
	MigrationPhaseConnectivityCheckFailed MigrationPhase = "ConnectivityCheckFailed"
)

// MigrationStatus is written to status.migration during a migration dry run.
type MigrationStatus struct {
	Phase   MigrationPhase `json:"phase"`
	Message string         `json:"message,omitempty"`
	Reason  string         `json:"reason,omitempty"`
}
