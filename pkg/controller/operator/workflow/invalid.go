package workflow

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// invalidStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type invalidStatus struct {
	commonStatus
	targetPhase mdbv1.Phase
}

func Invalid(msg string, params ...interface{}) *invalidStatus {
	return &invalidStatus{commonStatus: newCommonStatus(msg, params...), targetPhase: mdbv1.PhaseFailed}
}

func (f *invalidStatus) WithWarnings(warnings []mdbv1.StatusWarning) *invalidStatus {
	f.warnings = warnings
	return f
}

// WithTargetPhase allows to override the default phase for "invalid" (Failed) to another one.
// Most of all it may be Pending
func (f *invalidStatus) WithTargetPhase(targetPhase mdbv1.Phase) *invalidStatus {
	f.targetPhase = targetPhase
	return f
}

func (f invalidStatus) ReconcileResult() (reconcile.Result, error) {
	// We don't requeue validation failures
	return reconcile.Result{}, nil
}

func (f invalidStatus) IsOK() bool {
	return false
}

func (f invalidStatus) Merge(other Status) Status {
	switch v := other.(type) {
	// errors are concatenated
	case invalidStatus:
		return mergedInvalid(f, v)
	}
	// Invalid spec error dominates over anything else - there's no point in retrying until the spec is fixed
	return f
}
func (f invalidStatus) OnErrorPrepend(msg string) Status {
	f.commonStatus.prependMsg(msg)
	return f
}

func (f invalidStatus) StatusOptions() []mdbv1.StatusOption {
	options := f.statusOptions()
	// Add any specific options here
	return options
}

func (f invalidStatus) Phase() mdbv1.Phase {
	return f.targetPhase
}

func (f invalidStatus) Log(log *zap.SugaredLogger) {
	log.Error(util.UpperCaseFirstChar(f.msg))
}

func mergedInvalid(p1, p2 invalidStatus) invalidStatus {
	p := Invalid(p1.msg + ", " + p2.msg)
	p.warnings = append(p1.warnings, p2.warnings...)
	// Choosing one of the non-empty target phases
	p.targetPhase = p2.targetPhase
	if p1.targetPhase != "" {
		p.targetPhase = p1.targetPhase
	}
	return *p
}
