package workflow

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// okStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type okStatus struct {
	commonStatus
}

func OK() *okStatus {
	return &okStatus{}
}

func (o *okStatus) WithWarnings(warnings []status.Warning) *okStatus {
	o.warnings = warnings
	return o
}

func (o okStatus) ReconcileResult() (reconcile.Result, error) {
	return reconcile.Result{}, nil
}

func (o okStatus) IsOK() bool {
	return true
}

func (o okStatus) Merge(other Status) Status {
	// any other status takes precedence over OK
	return other
}

func (o okStatus) OnErrorPrepend(_ string) Status {
	return o
}

func (o okStatus) StatusOptions() []status.Option {
	return o.statusOptions()
}

func (f okStatus) Log(_ *zap.SugaredLogger) {
	// Doing no logging - the reconciler will do instead
}

func (o okStatus) Phase() status.Phase {
	return status.PhaseRunning
}
