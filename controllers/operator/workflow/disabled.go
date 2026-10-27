package workflow

import (
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// disabledStatus indicates that the subresource is not enabled
type disabledStatus struct {
	*okStatus
}

func Disabled() *disabledStatus {
	return &disabledStatus{okStatus: &okStatus{requeueAfter: util.TWENTY_FOUR_HOURS}}
}

func (d disabledStatus) Phase() status.Phase {
	return status.PhaseDisabled
}
