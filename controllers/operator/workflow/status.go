package workflow

import (
	"fmt"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/apierrors"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
)

// Status serves as a container holding the status of the custom resource
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

	// StatusOptions returns options that can be used to populate the CR status
	StatusOptions() []status.Option

	// Phase is the phase the status should get
	Phase() status.Phase

	// ReconcileResult returns the result of reconciliation to be returned by main controller
	ReconcileResult() (reconcile.Result, error)

	// Log performs logging of the status at some level if necessary
	Log(log *zap.SugaredLogger)
}

type commonStatus struct {
	msg               string
	warnings          []status.Warning
	resourcesNotReady []status.ResourceNotReady
	options           []status.Option
}

func newCommonStatus(msg string, params ...interface{}) commonStatus {
	return commonStatus{msg: fmt.Sprintf(msg, params...)}
}

func (c *commonStatus) prependMsg(msg string) {
	c.msg = msg + " " + c.msg
}

func (c commonStatus) statusOptions() []status.Option {
	// don't display any message on the MongoDB resource if the error is transient.
	msg := c.msg
	if apierrors.IsTransientMessage(msg) {
		msg = ""
	}
	options := []status.Option{
		status.NewMessageOption(msg),
		status.NewWarningsOption(c.warnings),
		status.NewResourcesNotReadyOption(c.resourcesNotReady),
	}
	return append(options, c.options...)
}

func ContainsPVCOption(options []status.Option) bool {
	if _, exists := status.GetOption(options, status.PVCStatusOption{}); exists {
		return true
	}
	return false
}
