package workflow

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// reconcileStatus serves as a container holding the status of the custom resource
// The main reason why it's needed is to allow to pass the information about the state of the resource back to the
// calling functions up to the top-level 'reconcile' avoiding multiple return parameters and 'if' statements
type Status interface {
	// Merge performs the Merge of current status with the status returned from the other operation and returns the
	// new status
	Merge(other Status) Status

	// IsOK returns true if there was no signal to interrupt reconciliation process
	IsOK() bool

	// OnErrorPrepend prepends the msg in the case of an error reconcileStatus
	OnErrorPrepend(msg string) Status

	// Returns options that can be used to populate the CR status
	StatusOptions() []mdbv1.StatusOption

	// Phase is the phase the status should get
	Phase() mdbv1.Phase

	// ReconcileResult returns the result of reconciliation to be returned by main controller
	ReconcileResult() (reconcile.Result, error)

	// Log performs logging of the status at some level if necessary
	Log(log *zap.SugaredLogger)
}

type commonStatus struct {
	msg               string
	warnings          []mdbv1.StatusWarning
	resourcesNotReady []mdbv1.ResourceNotReady
}

func newCommonStatus(msg string, params ...interface{}) commonStatus {
	return commonStatus{msg: fmt.Sprintf(msg, params...)}
}

func (c *commonStatus) prependMsg(msg string) {
	c.msg = msg + " " + c.msg
}

func (c commonStatus) statusOptions() []mdbv1.StatusOption {
	return []mdbv1.StatusOption{
		mdbv1.NewMessageOption(c.msg),
		mdbv1.NewWarningsOption(c.warnings),
		mdbv1.NewResourcesNotReadyOption(c.resourcesNotReady),
	}
}
