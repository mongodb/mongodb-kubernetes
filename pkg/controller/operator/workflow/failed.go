package workflow

import (
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
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

func (f *failedStatus) WithWarnings(warnings []mdbv1.StatusWarning) *failedStatus {
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

func (f failedStatus) StatusOptions() []mdbv1.StatusOption {
	options := f.statusOptions()
	// Add any specific options here
	return options
}

func (f failedStatus) Phase() mdbv1.Phase {
	return mdbv1.PhaseFailed
}

func (f failedStatus) Log(log *zap.SugaredLogger) {
	log.Error(stringutil.UpperCaseFirstChar(f.msg))
}

func mergedFailed(p1, p2 failedStatus) failedStatus {
	p := Failed(p1.msg + ", " + p2.msg)
	p.warnings = append(p1.warnings, p2.warnings...)
	return *p
}
