package workflow

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// reconcilingStatus indicates that the reconciliation process has started
type reconcilingStatus struct {
	commonStatus
	eraseMessage bool
}

func Reconciling() *reconcilingStatus {
	return &reconcilingStatus{}
}

// WithResourcesNotReady is intended to explicitly remove resourcesNotReady field from status as soon
// as the resources are ready
func (p *reconcilingStatus) WithResourcesNotReady(resourcesNotReady []mdbv1.ResourceNotReady) *reconcilingStatus {
	p.resourcesNotReady = resourcesNotReady
	return p
}

// WithNoMessage allows to explicitly erase the message in the status. This can be valuable in case the message is
// not relevant any more (e.g. StatefulSet was created)
func (p *reconcilingStatus) WithNoMessage() *reconcilingStatus {
	p.eraseMessage = true
	return p
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
	options := []mdbv1.StatusOption{}
	// We will override fields only if they were specified explicitly
	if o.resourcesNotReady != nil {
		options = append(options, mdbv1.NewResourcesNotReadyOption(o.resourcesNotReady))
	}
	if o.eraseMessage {
		options = append(options, mdbv1.NewMessageOption(""))
	}
	return options
}

func (f reconcilingStatus) Log(_ *zap.SugaredLogger) {
	// Doing no logging - the reconciler will do instead
}

func (o reconcilingStatus) Phase() mdbv1.Phase {
	return mdbv1.PhaseReconciling
}
