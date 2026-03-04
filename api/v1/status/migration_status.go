package status

// MigrationPhase describes the current phase of a VM→K8s migration dry run.
type MigrationPhase string

const (
	MigrationPhaseDryRunComplete MigrationPhase = "DryRunComplete"
	MigrationPhaseDryRunFailed   MigrationPhase = "DryRunFailed"
)

// DiffEntry represents a single changed field in the automation config.
type DiffEntry struct {
	// Path is the dot-separated path to the changed field, e.g. "processes[0].args2_6.net.tls.mode"
	Path string `json:"path"`
	// From is the previous value (only set for modified and removed entries)
	From string `json:"from,omitempty"`
	// To is the new value (only set for added and modified entries)
	To string `json:"to,omitempty"`
}

// AutomationConfigDiff holds the full diff between the current OM automation config
// and what the operator would produce when reconciling this CR.
type AutomationConfigDiff struct {
	Added    []DiffEntry `json:"added,omitempty"`
	Modified []DiffEntry `json:"modified,omitempty"`
	Removed  []DiffEntry `json:"removed,omitempty"`
	// Warning is set when the diff contains potentially destructive changes, e.g.
	// existing OM processes would be removed. Empty when no warnings.
	Warning string `json:"warning,omitempty"`
}

// MigrationStatus is written to status.migration during a dry run.
type MigrationStatus struct {
	Phase   MigrationPhase `json:"phase"`
	Message string         `json:"message,omitempty"`
	// AutomationConfigDiff is populated after a successful dry run.
	// It shows every field the operator would add, modify, or remove in OM.
	AutomationConfigDiff *AutomationConfigDiff `json:"automationConfigDiff,omitempty"`
}
