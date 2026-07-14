package status

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Phase string

const (
	// PhasePending means the reconciliation has finished but the resource is neither in Error nor Running state -
	// most of all waiting for some event to happen (CSRs approved, shard rebalanced etc)
	PhasePending Phase = "Pending"

	// PhaseRunning means the Mongodb Resource is in a running state
	PhaseRunning Phase = "Running"

	// PhaseFailed means the Mongodb Resource is in a failed state
	PhaseFailed Phase = "Failed"

	// PhaseDisabled means that the resource is not enabled
	PhaseDisabled Phase = "Disabled"

	// PhaseUpdated means a MongoDBUser was successfully updated
	PhaseUpdated Phase = "Updated"

	// PhaseUnsupported means a resource is not supported by the current Operator version
	PhaseUnsupported Phase = "Unsupported"
)

type Updater interface {
	GetStatusPath(options ...Option) string
	GetStatus(options ...Option) interface{}
	UpdateStatus(phase Phase, statusOptions ...Option)
	GetCommonStatus(options ...Option) *Common
	client.Object
}
