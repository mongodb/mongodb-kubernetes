package operator

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// reconcileStatus serves as a container holding the status of the custom resource
// The main reason why it's needed is to allow to pass the information about the state of the resource back to the
// calling functions up to the top-level 'reconcile' avoiding multiple return parameters and 'if' statements
type reconcileStatus interface {
	// updateStatus performs the update of the CR status in Kubernetes, returns the reconciliation result to return
	// from reconciliation loop
	updateStatus(resource Updatable, c *ReconcileCommonController, log *zap.SugaredLogger) (reconcile.Result, error)

	// merge performs the merge of current status with the status returned from the other operation and returns the
	// new status
	merge(other reconcileStatus) reconcileStatus

	// isOk returns true if there was no signal to interrupt reconciliation process
	isOk() bool
}

// successStatus indicates that the reconciliation process can proceed
type successStatus struct {
	msg string
}

// pendingStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type pendingStatus struct {
	msg string
}

// errorStatus indicates that the reconciliation process must be suspended and CR should get "Failed" status
type errorStatus struct {
	err               error
	retryAfterSeconds *time.Duration
}

type validationStatus struct {
	errorStatus
}

func ok() *successStatus {
	return &successStatus{}
}

func pending(msg string, params ...interface{}) *pendingStatus {
	return &pendingStatus{msg: fmt.Sprintf(msg, params...)}
}

func failed(msg string, params ...interface{}) *errorStatus {
	return &errorStatus{err: fmt.Errorf(msg, params...)}
}

func failedRetry(msg string, retryInSeconds time.Duration, params ...interface{}) *errorStatus {
	return &errorStatus{err: fmt.Errorf(msg, params...), retryAfterSeconds: &retryInSeconds}
}

func failedErr(err error) *errorStatus {
	return &errorStatus{err: err}
}

func failedValidation(msg string, params ...interface{}) *validationStatus {
	return &validationStatus{errorStatus: *failedErr(fmt.Errorf(msg, params...))}
}

func (e *pendingStatus) updateStatus(resource Updatable, c *ReconcileCommonController, log *zap.SugaredLogger) (reconcile.Result, error) {
	return c.updateStatusPending(resource, e.msg, log)
}

// merge performs messages concatenation for two pending results and makes sure the error message always overrides it
// So if for example the Sharded Cluster (tls enabled) reconciliation is happening and two statefulsets have pending
// CSRs (so two pending statuses were merged together) but at some stage the error happens - the new error will just
// override the pending ones and the CR will get "failed" phase
func (e *pendingStatus) merge(other reconcileStatus) reconcileStatus {
	switch v := other.(type) {
	// pending messages are just merged together
	case *pendingStatus:
		return pending(e.msg + ", " + v.msg)
	// any error message overrides the others
	case *errorStatus, *validationStatus:
		return v
	}
	return e
}
func (e *pendingStatus) isOk() bool {
	return false
}

func (e *errorStatus) updateStatus(resource Updatable, c *ReconcileCommonController, log *zap.SugaredLogger) (reconcile.Result, error) {
	result, error := c.updateStatusFailed(resource, e.err.Error(), log)
	if e.retryAfterSeconds != nil {
		return reconcile.Result{RequeueAfter: time.Second * (*e.retryAfterSeconds)}, nil
	}
	return result, error
}

func (e *errorStatus) merge(other reconcileStatus) reconcileStatus {
	// any error message overrides the others
	switch v := other.(type) {
	case *pendingStatus:
		return e
	case *errorStatus, *validationStatus:
		return v
	}
	return e
}
func (e *errorStatus) isOk() bool {
	return false
}

func (e *successStatus) updateStatus(resource Updatable, c *ReconcileCommonController, log *zap.SugaredLogger) (reconcile.Result, error) {
	return c.updateStatusSuccessful(resource, log)
}

func (e *successStatus) merge(other reconcileStatus) reconcileStatus {
	return other
}
func (e *successStatus) isOk() bool {
	return true
}

func (e *validationStatus) updateStatus(resource Updatable, c *ReconcileCommonController, log *zap.SugaredLogger) (reconcile.Result, error) {
	return c.updateStatusValidationFailure(resource, e.err.Error(), log)
}
