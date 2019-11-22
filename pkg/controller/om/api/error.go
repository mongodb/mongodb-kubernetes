package api

import (
	"fmt"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// Error is the error extension that contains the details of OM error if OM returned the error. This allows the
// code using Connection methods to do more fine-grained exception handling depending on exact error that happened.
// The class has to encapsulate the usual error (non-OM one) as well as the error may happen at any stage before/after
// OM request (failing to (de)serialize json object for example) so in this case all fields except for 'Detail' will be
// empty
type Error struct {
	Status    *int   `json:"error"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
	ErrorCode string `json:"errorCode"`
}

// NewError
func NewError(err error) error {
	if err == nil {
		return nil
	}
	return &Error{Detail: err.Error()}
}

func (e *Error) IsGeneric() bool {
	return strings.Contains(e.Error(), util.GenericErrorMessage)
}

// Error
func (e *Error) Error() string {
	if e.Status != nil {
		msg := fmt.Sprintf("Status: %d", *e.Status)
		if e.Reason != "" {
			msg += fmt.Sprintf(" (%s)", e.Reason)
		}
		if e.ErrorCode != "" {
			msg += fmt.Sprintf(", ErrorCode: %s", e.ErrorCode)
		}
		if e.Detail != "" {
			msg += fmt.Sprintf(", Detail: %s", e.Detail)
		}
		return msg
	}
	return e.Detail
}

// ErrorCodeIn
func (e *Error) ErrorCodeIn(errorCodes ...string) bool {
	for _, c := range errorCodes {
		if e.ErrorCode == c {
			return true
		}
	}
	return false
}
