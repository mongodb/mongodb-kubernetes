package workflow

import (
	"time"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/apierrors"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// failedStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type failedStatus struct {
	commonStatus
	retryInSeconds time.Duration
}

func Failed(msg string, params ...interface{}) *failedStatus {
	return &failedStatus{commonStatus: newCommonStatus(msg, params...), retryInSeconds: 10}
}

func (f *failedStatus) WithWarnings(warnings []status.Warning) *failedStatus {
	f.warnings = warnings
	return f
}

func (f *failedStatus) WithRetry(retryInSeconds time.Duration) *failedStatus {
	f.retryInSeconds = retryInSeconds
	return f
}

func (f failedStatus) ReconcileResult() (reconcile.Result, error) {
	return reconcile.Result{RequeueAfter: time.Second * f.retryInSeconds}, nil
}

func (f failedStatus) IsOK() bool {
	return false
}

func (f failedStatus) Merge(other Status) Status {
	switch v := other.(type) {
	// errors are concatenated
	case failedStatus:
		return mergedFailed(f, v)
	case invalidStatus:
		return other
	}
	return f
}
func (f failedStatus) OnErrorPrepend(msg string) Status {
	f.commonStatus.prependMsg(msg)
	return f
}

func (f failedStatus) StatusOptions() []status.Option {
	options := f.statusOptions()
	// Add any specific options here
	return options
}

func (f failedStatus) Phase() status.Phase {
	if apierrors.IsTransientMessage(f.msg) {
		return status.PhasePending
	}
	return status.PhaseFailed
}

func (f failedStatus) Log(log *zap.SugaredLogger) {
	log.Error(stringutil.UpperCaseFirstChar(f.msg))
}

func mergedFailed(p1, p2 failedStatus) failedStatus {
	p := Failed(p1.msg + ", " + p2.msg)
	p.warnings = append(p1.warnings, p2.warnings...)
	return *p
}
