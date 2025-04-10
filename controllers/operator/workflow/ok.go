package workflow

import (
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// okStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type okStatus struct {
	commonStatus
	requeue      bool
	requeueAfter time.Duration
}

func OK() *okStatus {
	return &okStatus{requeueAfter: util.TWENTY_FOUR_HOURS}
}

func (o *okStatus) WithWarnings(warnings []status.Warning) *okStatus {
	o.warnings = warnings
	return o
}

func (o *okStatus) WithAdditionalOptions(options ...status.Option) *okStatus {
	o.options = options
	return o
}

func (o *okStatus) ReconcileResult() (reconcile.Result, error) {
	return reconcile.Result{Requeue: o.requeue, RequeueAfter: o.requeueAfter}, nil
}

func (o *okStatus) IsOK() bool {
	return !o.requeue
}

func (o *okStatus) Merge(other Status) Status {
	// any other status takes precedence over OK
	return other
}

func (o *okStatus) OnErrorPrepend(_ string) Status {
	return o
}

func (o *okStatus) StatusOptions() []status.Option {
	return o.statusOptions()
}

func (o *okStatus) Log(_ *zap.SugaredLogger) {
	// Doing no logging - the reconciler will do instead
}

func (o *okStatus) Phase() status.Phase {
	return status.PhaseRunning
}

func (o *okStatus) Requeue() Status {
	o.requeueAfter = 0
	o.requeue = true
	return o
}
