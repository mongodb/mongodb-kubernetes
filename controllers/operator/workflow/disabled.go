package workflow

import (
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

// disabledStatus indicates that the subresource is not enabled
type disabledStatus struct {
	*okStatus
}

func Disabled() *disabledStatus {
	return &disabledStatus{okStatus: &okStatus{requeue: false}}
}

func (d disabledStatus) Phase() status.Phase {
	return status.PhaseDisabled
}
