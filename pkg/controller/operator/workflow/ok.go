package workflow

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// okStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type okStatus struct {
	warnings []mdbv1.StatusWarning
}

func OK() *okStatus {
	return &okStatus{}
}

func (o *okStatus) WithWarnings(warnings []mdbv1.StatusWarning) *okStatus {
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

func (o okStatus) StatusOptions() []mdbv1.StatusOption {
	return []mdbv1.StatusOption{mdbv1.NewWarningsOption(o.warnings)}
}

func (f okStatus) Log(_ *zap.SugaredLogger) {
	// Doing no logging - the reconciler will do instead
}

func (o okStatus) Phase() mdbv1.Phase {
	return mdbv1.PhaseRunning
}
