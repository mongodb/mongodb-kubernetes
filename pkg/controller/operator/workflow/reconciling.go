package workflow

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// reconcilingStatus indicates that the reconciliation process has started
type reconcilingStatus struct {
}

func Reconciling() *reconcilingStatus {
	return &reconcilingStatus{}
}

func (o reconcilingStatus) ReconcileResult() (reconcile.Result, error) {
	// not expected to be called
	return reconcile.Result{}, nil
}

func (o reconcilingStatus) IsOK() bool {
	return true
}

func (o reconcilingStatus) Merge(other Status) Status {
	// any other status takes precedence over Reconciling
	return other
}

func (o reconcilingStatus) OnErrorPrepend(_ string) Status {
	return o
}

func (o reconcilingStatus) StatusOptions() []mdbv1.StatusOption {
	return []mdbv1.StatusOption{}
}

func (f reconcilingStatus) Log(_ *zap.SugaredLogger) {
	// Doing no logging - the reconciler will do instead
}

func (o reconcilingStatus) Phase() mdbv1.Phase {
	return mdbv1.PhaseReconciling
}
