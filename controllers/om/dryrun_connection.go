package om

import (
	"reflect"
	"sync"

	"github.com/r3labs/diff/v3"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// DryRunOmConnection wraps a real Connection with a stateful shadow deployment.
// All reads return the shadow (seeded once from real OM on first read). All writes
// update the shadow and record the diff without pushing to OpsManager. This allows
// a full reconcile — including sequential auth steps with WaitForReadyState — to
// run end-to-end in dry-run mode, accumulating would-be changes without side effects.
//
// Go embedding does not intercept internal method calls, so each write path
// (ReadUpdateDeployment, ReadUpdateAutomationConfig, UpdateAutomationConfig,
// UpdateDeployment) is explicitly overridden.
type DryRunOmConnection struct {
	Connection
	log      *zap.SugaredLogger
	onDryRun func(changelog diff.Changelog)
	mu       sync.Mutex
	shadow   Deployment // nil until seeded from real OM on first read
}

// NewDryRunConnection creates a stateful dry-run wrapper. onDryRun is invoked with
// each intercepted diff (after the caller applies any filtering); pass nil to log only.
func NewDryRunConnection(conn Connection, log *zap.SugaredLogger, onDryRun func(diff.Changelog)) Connection {
	return &DryRunOmConnection{
		Connection: conn,
		log:        log,
		onDryRun:   onDryRun,
	}
}

// getShadow seeds the shadow from the real connection on first call, then returns it.
// Callers must hold d.mu.
func (d *DryRunOmConnection) getShadow() (Deployment, error) {
	if d.shadow == nil {
		real, err := d.Connection.ReadDeployment()
		if err != nil {
			return nil, err
		}
		d.shadow = real
	}
	return d.shadow, nil
}

// record computes the diff between from and to, logs it, and calls onDryRun.
func (d *DryRunOmConnection) record(from, to Deployment) {
	changelog, err := diff.Diff(from, to, diff.AllowTypeMismatch(true))
	if err != nil {
		d.log.Warnw("AC dry-run: failed to compute diff", "error", err)
		return
	}
	d.log.Infow("AC dry-run: would push deployment changes (not applied)", "num_changes", len(changelog))
	if d.onDryRun != nil {
		d.onDryRun(changelog)
	}
}

// ReadDeployment returns the shadow deployment (seeding from real OM on first call).
func (d *DryRunOmConnection) ReadDeployment() (Deployment, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.getShadow()
}

// ReadAutomationConfig builds an AutomationConfig from the shadow deployment.
func (d *DryRunOmConnection) ReadAutomationConfig() (*AutomationConfig, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	shadow, err := d.getShadow()
	if err != nil {
		return nil, err
	}
	return BuildAutomationConfigFromDeployment(shadow)
}

// ReadUpdateDeployment applies fn to the shadow deployment and records the diff.
// No write is made to OpsManager.
func (d *DryRunOmConnection) ReadUpdateDeployment(changeDeploymentFunc func(Deployment) error, log *zap.SugaredLogger) error {
	mutex := GetMutex(d.Connection.GroupName(), d.Connection.OrgID())
	mutex.Lock()
	defer mutex.Unlock()

	d.mu.Lock()
	defer d.mu.Unlock()

	shadow, err := d.getShadow()
	if err != nil {
		return err
	}

	original, err := util.MapDeepCopy(shadow)
	if err != nil {
		return err
	}

	// isEqualAfterModification applies changeDeploymentFunc to shadow in place.
	isEqual, err := isEqualAfterModification(changeDeploymentFunc, shadow)
	if err != nil {
		return err
	}
	if isEqual {
		log.Debug("AC dry-run: AutomationConfig has not changed, nothing to record")
		return nil
	}

	// shadow now holds the would-be state (modified in place by isEqualAfterModification).
	d.record(original, shadow)
	return nil
}

// ReadUpdateAutomationConfig applies modifyACFunc to the shadow AC and records the diff.
// No write is made to OpsManager.
func (d *DryRunOmConnection) ReadUpdateAutomationConfig(modifyACFunc func(ac *AutomationConfig) error, log *zap.SugaredLogger) error {
	mutex := GetMutex(d.Connection.GroupName(), d.Connection.OrgID())
	mutex.Lock()
	defer mutex.Unlock()

	d.mu.Lock()
	defer d.mu.Unlock()

	shadow, err := d.getShadow()
	if err != nil {
		log.Errorf("AC dry-run: error reading shadow deployment: %s", err)
		return err
	}

	ac, err := BuildAutomationConfigFromDeployment(shadow)
	if err != nil {
		return err
	}

	original, err := BuildAutomationConfigFromDeployment(shadow)
	if err != nil {
		return err
	}

	if err := modifyACFunc(ac); err != nil {
		return err
	}

	if !reflect.DeepEqual(original.Deployment, ac.Deployment) {
		panic("It seems you modified the deployment directly. This is not allowed. Please use helper objects instead.")
	}

	if original.EqualsWithoutDeployment(*ac) {
		log.Debug("AC dry-run: AutomationConfig has not changed, nothing to record")
		return nil
	}

	if err := ac.Apply(); err != nil {
		return err
	}

	originalDep, err := util.MapDeepCopy(shadow)
	if err != nil {
		return err
	}

	// Update shadow to the would-be state.
	for k, v := range ac.Deployment {
		shadow[k] = v
	}

	d.record(originalDep, shadow)
	return nil
}

// UpdateAutomationConfig applies the AC and records the diff against shadow. No PUT.
func (d *DryRunOmConnection) UpdateAutomationConfig(ac *AutomationConfig, log *zap.SugaredLogger) error {
	if err := ac.Apply(); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	shadow, err := d.getShadow()
	if err != nil {
		d.log.Warnw("AC dry-run: failed to read shadow for diff", "error", err)
		return nil
	}

	original, err := util.MapDeepCopy(shadow)
	if err != nil {
		return err
	}

	for k, v := range ac.Deployment {
		shadow[k] = v
	}

	d.record(original, shadow)
	return nil
}

// UpdateDeployment records the diff against shadow and updates shadow. No PUT.
func (d *DryRunOmConnection) UpdateDeployment(incoming Deployment) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	shadow, err := d.getShadow()
	if err != nil {
		d.log.Warnw("AC dry-run: failed to read shadow for diff", "error", err)
		return nil, nil
	}

	original, err := util.MapDeepCopy(shadow)
	if err != nil {
		return nil, err
	}

	for k, v := range incoming {
		shadow[k] = v
	}

	d.record(original, shadow)
	return nil, nil
}

// ReadUpdateBackupAgentConfig is a no-op in dry-run mode. The backup agent config
// lives in the AC but is managed separately; calling the real OM API would fail
// because the shadow changes have not been pushed (e.g., x509 sslPEMKeyFile
// validation requires the AC to already be in the would-be state).
func (d *DryRunOmConnection) ReadUpdateBackupAgentConfig(backupFunc func(*BackupAgentConfig) error, log *zap.SugaredLogger) error {
	return nil
}

// ReadUpdateMonitoringAgentConfig is a no-op in dry-run mode for the same reason
// as ReadUpdateBackupAgentConfig.
func (d *DryRunOmConnection) ReadUpdateMonitoringAgentConfig(matFunc func(*MonitoringAgentConfig) error, log *zap.SugaredLogger) error {
	return nil
}

// ReadUpdateAgentsLogRotation is a no-op in dry-run mode.
func (d *DryRunOmConnection) ReadUpdateAgentsLogRotation(logRotateSetting mdbv1.AgentConfig, log *zap.SugaredLogger) error {
	return nil
}

// ReadAutomationStatus returns an empty status so that WaitForReadyState passes
// immediately. Processes not present in the status are treated as having reached
// goal state (see automation_status.go checkAutomationStatusIsGoal comment).
func (d *DryRunOmConnection) ReadAutomationStatus() (*AutomationStatus, error) {
	return &AutomationStatus{GoalVersion: 0, Processes: []ProcessStatus{}}, nil
}
