package workflow

import (
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

// unsupportedStatus indicates that the subresource is not supported by the current Operator
type unsupportedStatus struct {
	*okStatus
}

func Unsupported(msg string, params ...interface{}) *unsupportedStatus {
	return &unsupportedStatus{okStatus: &okStatus{requeue: false, commonStatus: newCommonStatus(msg, params...)}}
}

func (d unsupportedStatus) Phase() status.Phase {
	return status.PhaseUnsupported
}
