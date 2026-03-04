package om

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/r3labs/diff/v3"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
)

// DryRunConnection wraps a real Connection and intercepts all writes.
// Reads delegate to the real connection; writes are no-ops captured in-memory.
// Call Result() after the reconcile flow to get the net automation config diff.
type DryRunConnection struct {
	Connection
	mu       sync.Mutex
	baseline Deployment
	working  Deployment
	fetched  bool
}

var _ Connection = &DryRunConnection{}

// NewDryRunConnection creates a DryRunConnection wrapping the given real connection.
func NewDryRunConnection(real Connection) *DryRunConnection {
	return &DryRunConnection{Connection: real}
}

// ReadUpdateDeployment applies fn to the in-memory working copy without writing to OM.
func (d *DryRunConnection) ReadUpdateDeployment(fn func(Deployment) error, log *zap.SugaredLogger) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.fetched {
		dep, err := d.Connection.ReadDeployment()
		if err != nil {
			return err
		}
		d.baseline = dep
		d.working = dep.DeepCopy()
		d.fetched = true
	}

	return fn(d.working)
}

// UpdateDeployment is a no-op in dry-run mode.
func (d *DryRunConnection) UpdateDeployment(_ Deployment) ([]byte, error) {
	return nil, nil
}

// ReadUpdateAutomationConfig applies fn to an in-memory AutomationConfig built from
// the working deployment, then merges changes back without writing to OM.
func (d *DryRunConnection) ReadUpdateAutomationConfig(fn func(*AutomationConfig) error, log *zap.SugaredLogger) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.fetched {
		dep, err := d.Connection.ReadDeployment()
		if err != nil {
			return err
		}
		d.baseline = dep
		d.working = dep.DeepCopy()
		d.fetched = true
	}

	ac, err := BuildAutomationConfigFromDeployment(d.working)
	if err != nil {
		return err
	}

	if err := fn(ac); err != nil {
		return err
	}

	if err := ac.Apply(); err != nil {
		return err
	}
	d.working = ac.Deployment

	return nil
}

// UpdateAutomationConfig is a no-op in dry-run mode.
func (d *DryRunConnection) UpdateAutomationConfig(_ *AutomationConfig, _ *zap.SugaredLogger) error {
	return nil
}

// ReadUpdateAgentsLogRotation is a no-op in dry-run mode.
func (d *DryRunConnection) ReadUpdateAgentsLogRotation(_ mdbv1.AgentConfig, _ *zap.SugaredLogger) error {
	return nil
}

// RemoveHost is a no-op in dry-run mode.
func (d *DryRunConnection) RemoveHost(_ string) error {
	return nil
}

// UpdateBackupConfig is a no-op in dry-run mode.
func (d *DryRunConnection) UpdateBackupConfig(config *backup.Config) (*backup.Config, error) {
	return config, nil
}

// UpdateBackupStatus is a no-op in dry-run mode.
func (d *DryRunConnection) UpdateBackupStatus(_ string, _ backup.Status) error {
	return nil
}

// UpdateSnapshotSchedule is a no-op in dry-run mode.
func (d *DryRunConnection) UpdateSnapshotSchedule(_ string, _ *backup.SnapshotSchedule) error {
	return nil
}

// UpdateGroupBackupConfig is a no-op in dry-run mode.
func (d *DryRunConnection) UpdateGroupBackupConfig(_ backup.GroupBackupConfig) ([]byte, error) {
	return nil, nil
}

// Result computes and returns the net automation config diff between the baseline
// (what OM currently has) and the working copy (what the operator would write).
// Returns an empty diff if ReadUpdateDeployment was never called.
func (d *DryRunConnection) Result() (*status.AutomationConfigDiff, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.fetched {
		return &status.AutomationConfigDiff{}, nil
	}

	return buildDiffResult(d.baseline, d.working)
}

// buildDiffResult computes the structured diff between baseline and modified deployments.
//
// Both deployments are first normalized through a JSON round-trip so that every value
// has a uniform Go type (float64 for numbers, bool, string, []interface{}, map[string]interface{}).
// This eliminates false "UPDATE" entries that arise when the operator stores typed values
// — such as float32 for priority, int for votes, or MongoType (a named string) for
// processType — that are semantically identical to their JSON-parsed float64/string
// counterparts but would otherwise be reported as modifications by r3labs/diff.
func buildDiffResult(baseline, modified Deployment) (*status.AutomationConfigDiff, error) {
	normalizedBaseline, err := normalizeDeployment(baseline)
	if err != nil {
		return nil, fmt.Errorf("normalizing baseline deployment: %w", err)
	}

	normalizedModified, err := normalizeDeployment(modified)
	if err != nil {
		return nil, fmt.Errorf("normalizing modified deployment: %w", err)
	}

	changelog, err := diff.Diff(normalizedBaseline, normalizedModified,
		diff.SliceOrdering(false),  // reordering slice elements is not a meaningful change
		diff.DisableStructValues(), // when a struct is new/deleted, emit one entry not one per field
	)
	if err != nil {
		return nil, fmt.Errorf("computing automation config diff: %w", err)
	}

	// Strip paths that are always OM-managed or contain sensitive material and
	// would flood the diff with irrelevant noise on every reconcile.
	// FilterOut does a regexp prefix match per path segment, so ".*" matches any
	// array index and each call strips all changelog entries under that prefix.
	for _, noisePath := range [][]string{
		// OM's entire MongoDB binary download catalog — the operator never modifies this.
		{"mongoDbVersions"},
		// Computed SCRAM credentials and plain-text passwords stored per user —
		// not meaningful change signals for migration review.
		{"auth", "usersWanted", ".*", "scramSha256Creds"},
		{"auth", "usersWanted", ".*", "scramSha1Creds"},
		{"auth", "usersWanted", ".*", "pwd"},
	} {
		changelog = changelog.FilterOut(noisePath)
	}

	result := &status.AutomationConfigDiff{}

	for _, change := range changelog {
		// Skip transitions between semantically empty values (nil ↔ [] ↔ {}).
		// For CREATE entries change.From is always nil; for DELETE entries change.To
		// is always nil — so this single guard covers all three change types:
		//   CREATE  to:[]   → From=nil(empty), To=[](empty)   → skip
		//   DELETE  from:[] → From=[](empty),  To=nil(empty)  → skip
		//   UPDATE  nil↔[]  → both empty                      → skip
		if isEmptyLike(change.From) && isEmptyLike(change.To) {
			continue
		}

		path := strings.Join(change.Path, ".")
		switch change.Type {
		case diff.CREATE:
			result.Added = append(result.Added, status.DiffEntry{
				Path: path,
				To:   fmt.Sprintf("%v", change.To),
			})
		case diff.UPDATE:
			result.Modified = append(result.Modified, status.DiffEntry{
				Path: path,
				From: fmt.Sprintf("%v", change.From),
				To:   fmt.Sprintf("%v", change.To),
			})
		case diff.DELETE:
			result.Removed = append(result.Removed, status.DiffEntry{
				Path: path,
				From: fmt.Sprintf("%v", change.From),
			})
		}
	}

	if hasProcessRemoval(baseline, modified) {
		result.Warning = "One or more existing OM processes would be removed. " +
			"If these are VM-managed members, add them to spec.externalMembers to preserve them during migration."
	}

	return result, nil
}

// isEmptyLike reports whether a diff value is semantically "nothing":
// nil, an empty slice ([]interface{}{}), or an empty map (map[string]interface{}{}).
// Used to suppress noise from nil↔[] and nil↔{} transitions that carry no real
// meaning in the automation config (e.g. backupVersions going from absent to []).
func isEmptyLike(v interface{}) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case []interface{}:
		return len(val) == 0
	case map[string]interface{}:
		return len(val) == 0
	}
	return false
}

// normalizeDeployment serializes the deployment through a JSON round-trip to produce a
// plain map[string]interface{} with uniform value types: float64 for all numbers, bool
// for booleans, string for strings, []interface{} for arrays, and map[string]interface{}
// for objects. This eliminates false positives in the diff that arise when the operator
// inserts typed values (e.g. float32, int, MongoType, map[string]string) into the working
// Deployment that are semantically equal to their JSON-unmarshaled counterparts but have
// different Go types.
func normalizeDeployment(d Deployment) (map[string]interface{}, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// hasProcessRemoval returns true when the modified deployment has fewer processes
// than the baseline — i.e. the reconcile function would remove existing OM processes.
// getProcesses() is used instead of a raw []interface{} type assertion because the
// working deployment may store processes as []Process after the operator reconciles.
func hasProcessRemoval(baseline, modified Deployment) bool {
	return len(modified.getProcesses()) < len(baseline.getProcesses())
}
