package workflow

import (
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
)

// invalidStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type invalidStatus struct {
	commonStatus
	targetPhase status.Phase
}

func Invalid(msg string, params ...interface{}) *invalidStatus {
	return &invalidStatus{commonStatus: newCommonStatus(msg, params...), targetPhase: status.PhaseFailed}
}

func (f *invalidStatus) WithWarnings(warnings []status.Warning) *invalidStatus {
	f.warnings = warnings
	return f
}

// WithTargetPhase allows to override the default phase for "invalid" (Failed) to another one.
// Most of all it may be Pending
func (f *invalidStatus) WithTargetPhase(targetPhase status.Phase) *invalidStatus {
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

func (f invalidStatus) StatusOptions() []status.Option {
	options := f.statusOptions()
	// Add any specific options here
	return options
}

func (f invalidStatus) Phase() status.Phase {
	return f.targetPhase
}

func (f invalidStatus) Log(log *zap.SugaredLogger) {
	log.Error(stringutil.UpperCaseFirstChar(f.msg))
}

func mergedInvalid(p1, p2 invalidStatus) invalidStatus {
	p := Invalid("%s, %s", p1.msg, p2.msg)
	p.warnings = append(p1.warnings, p2.warnings...)
	// Choosing one of the non-empty target phases
	p.targetPhase = p2.targetPhase
	if p1.targetPhase != "" {
		p.targetPhase = p1.targetPhase
	}
	return *p
}
