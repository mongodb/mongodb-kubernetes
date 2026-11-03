package workflow

import (
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	omapierror "github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/apierrors"
)

// automationConfigConflictRetry is the requeue delay after another writer changed the
// automation config. The next reconcile re-reads the config and recomputes the desired
// state, so a short delay is enough to converge.
const automationConfigConflictRetry = 2 * time.Second

// failedStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type failedStatus struct {
	commonStatus
	retryInSeconds int
	// err contains error with stacktrace
	err error
	// automationConfigConflict is true when another writer changed the automation
	// config between the operator's read and write. That is retryable, not a failure.
	automationConfigConflict bool
}

func Failed(err error, params ...any) *failedStatus {
	return &failedStatus{
		commonStatus:             newCommonStatus(err.Error(), params...),
		err:                      err,
		retryInSeconds:           10,
		automationConfigConflict: omapierror.IsAutomationConfigConflict(err),
	}
}

func (f *failedStatus) WithWarnings(warnings []status.Warning) *failedStatus {
	f.warnings = warnings
	return f
}

func (f *failedStatus) WithRetry(retryInSeconds int) *failedStatus {
	f.retryInSeconds = retryInSeconds
	return f
}

func (f *failedStatus) ReconcileResult() (reconcile.Result, error) {
	if f.automationConfigConflict {
		return reconcile.Result{RequeueAfter: automationConfigConflictRetry}, nil
	}
	return reconcile.Result{RequeueAfter: time.Second * time.Duration(f.retryInSeconds)}, nil
}

func (f *failedStatus) WithAdditionalOptions(options ...status.Option) *failedStatus {
	f.options = options
	return f
}

func (f *failedStatus) IsOK() bool {
	return false
}

func (f *failedStatus) Merge(other Status) Status {
	switch v := other.(type) {
	// errors are concatenated
	case *failedStatus:
		return mergedFailed(f, v)
	case *invalidStatus:
		return other
	}
	return f
}

func (f *failedStatus) OnErrorPrepend(msg string) Status {
	f.prependMsg(msg)
	return f
}

func (f *failedStatus) StatusOptions() []status.Option {
	// don't display any message on the MongoDB resource if the error is transient.
	options := f.statusOptions()
	return options
}

func (f *failedStatus) Phase() status.Phase {
	if f.automationConfigConflict || apierrors.IsTransientMessage(f.msg) {
		return status.PhasePending
	}
	return status.PhaseFailed
}

// Log does not take the f.msg but instead takes f.err to make sure we print the actual stack trace of the error.
func (f *failedStatus) Log(log *zap.SugaredLogger) {
	if f.automationConfigConflict {
		log.Infof("%v", f.err)
		return
	}
	log.Errorf("%+v", f.err)
}

func mergedFailed(p1, p2 *failedStatus) *failedStatus {
	msg := p1.msg + ", " + p2.msg
	p := Failed(xerrors.Errorf(msg))
	p.automationConfigConflict = p1.automationConfigConflict || p2.automationConfigConflict
	p.warnings = append(p1.warnings, p2.warnings...)
	return p
}
