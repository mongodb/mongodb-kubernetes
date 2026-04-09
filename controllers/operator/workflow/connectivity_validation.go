package workflow

import (
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

// connectivityValidationStatus indicates the resource is in migration dry-run (connectivity validation only).
// Phase is PhaseConnectivityValidation so it is clear the resource is still in that phase.
type connectivityValidationStatus struct {
	commonStatus
	retryInSeconds int
}

// ConnectivityValidation returns a status that sets phase to PhaseConnectivityValidation (migration dry-run).
func ConnectivityValidation(msg string, params ...interface{}) *connectivityValidationStatus {
	return &connectivityValidationStatus{commonStatus: newCommonStatus(msg, params...)}
}

// WithRetry sets the requeue interval in seconds (e.g. while the validation Job is running).
func (c *connectivityValidationStatus) WithRetry(retryInSeconds int) *connectivityValidationStatus {
	c.retryInSeconds = retryInSeconds
	return c
}

func (c *connectivityValidationStatus) ReconcileResult() (reconcile.Result, error) {
	if c.retryInSeconds > 0 {
		return reconcile.Result{RequeueAfter: time.Duration(c.retryInSeconds) * time.Second}, nil
	}
	return reconcile.Result{}, nil
}

func (c *connectivityValidationStatus) IsOK() bool {
	return c.retryInSeconds == 0
}

func (c *connectivityValidationStatus) Merge(other Status) Status {
	return other
}

func (c *connectivityValidationStatus) OnErrorPrepend(msg string) Status {
	c.prependMsg(msg)
	return c
}

func (c *connectivityValidationStatus) StatusOptions() []status.Option {
	return c.statusOptions()
}

func (c *connectivityValidationStatus) Phase() status.Phase {
	return status.PhaseConnectivityValidation
}

func (c *connectivityValidationStatus) Log(log *zap.SugaredLogger) {
	log.Info(stringutil.UpperCaseFirstChar(c.msg))
}
