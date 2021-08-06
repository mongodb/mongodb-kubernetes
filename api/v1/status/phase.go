package status

type Phase string

const (
	// PhaseReconciling means the controller is in the middle of reconciliation process
	PhaseReconciling Phase = "Reconciling"

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
