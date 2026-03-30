package om

import (
	"github.com/r3labs/diff/v3"
	"go.uber.org/zap"
)

// DryRunOmConnection wraps a real Connection and intercepts all writes to the
// OpsManager automationConfig API. Instead of pushing changes, it computes a
// diff of what would change and invokes onDryRun so the caller can record it.
// All read methods (ReadDeployment, ReadAutomationConfig, etc.) are delegated
// to the underlying connection unchanged.
type DryRunOmConnection struct {
	Connection
	log      *zap.SugaredLogger
	onDryRun func(changelog diff.Changelog)
}

// NewDryRunConnection creates a Connection wrapper that intercepts UpdateDeployment
// calls. onDryRun is invoked with the computed diff; pass nil to log only.
// All other Connection methods delegate to conn.
func NewDryRunConnection(conn Connection, log *zap.SugaredLogger, onDryRun func(diff.Changelog)) Connection {
	return &DryRunOmConnection{
		Connection: conn,
		log:        log,
		onDryRun:   onDryRun,
	}
}

// UpdateDeployment overrides the real implementation: instead of PUTing to
// OpsManager it reads the current deployment, diffs it against the incoming
// one, logs the result, and calls onDryRun. No write is made.
func (d *DryRunOmConnection) UpdateDeployment(incoming Deployment) ([]byte, error) {
	current, err := d.Connection.ReadDeployment()
	if err != nil {
		d.log.Warnw("AC dry-run: failed to read current deployment for diff", "error", err)
		return nil, nil
	}

	changelog, err := diff.Diff(current, incoming, diff.AllowTypeMismatch(true))
	if err != nil {
		d.log.Warnw("AC dry-run: failed to compute deployment diff", "error", err)
		return nil, nil
	}

	d.log.Infow("AC dry-run: would push deployment changes (not applied)",
		"num_changes", len(changelog),
		"changes", changelog,
	)

	if d.onDryRun != nil {
		d.onDryRun(changelog)
	}
	return nil, nil
}
