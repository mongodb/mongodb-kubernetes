package workflow

import (
	"time"

	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/apierrors"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// failedStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type failedStatus struct {
	commonStatus
	retryInSeconds int
	// err contains error with stacktrace
	err error
}

func Failed(err error, params ...interface{}) *failedStatus {
	return &failedStatus{commonStatus: newCommonStatus(err.Error(), params...), err: err, retryInSeconds: 10}
}

func (f *failedStatus) WithWarnings(warnings []status.Warning) *failedStatus {
	f.warnings = warnings
	return f
}

func (f *failedStatus) WithRetry(retryInSeconds int) *failedStatus {
	f.retryInSeconds = retryInSeconds
	return f
}

func (f failedStatus) ReconcileResult() (reconcile.Result, error) {
	return reconcile.Result{RequeueAfter: time.Second * time.Duration(f.retryInSeconds)}, nil
}

func (f *failedStatus) WithAdditionalOptions(options []status.Option) *failedStatus {
	f.options = options
	return f
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
	// don't display any message on the MongoDB resource if the error is transient.
	options := f.statusOptions()
	return options
}

func (f failedStatus) Phase() status.Phase {
	if apierrors.IsTransientMessage(f.msg) {
		return status.PhasePending
	}
	return status.PhaseFailed
}

// Log does not take the f.msg but instead takes f.err to make sure we print the actual stack trace of the error.
func (f failedStatus) Log(log *zap.SugaredLogger) {
	log.Errorf("%+v", f.err)
}

func mergedFailed(p1, p2 failedStatus) failedStatus {
	msg := p1.msg + ", " + p2.msg
	p := Failed(xerrors.Errorf(msg))
	p.warnings = append(p1.warnings, p2.warnings...)
	return *p
}
