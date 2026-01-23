package apierror

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// Error codes that Ops Manager may return that we are concerned about
	OrganizationNotFound       = "ORG_NAME_NOT_FOUND"
	ProjectNotFound            = "GROUP_NAME_NOT_FOUND"
	BackupDaemonConfigNotFound = "DAEMON_MACHINE_CONFIG_NOT_FOUND"
	UserAlreadyExists          = "USER_ALREADY_EXISTS"
	DuplicateWhitelistEntry    = "DUPLICATE_GLOBAL_WHITELIST_ENTRY"

	// BackupVersionNotAvailable is the message returned by Ops Manager when trying to start backup
	// before the monitoring agent has reported MongoDB version information. This is a transient
	// condition that resolves once the agent registers with Ops Manager.
	BackupVersionNotAvailable = "MongoDB version information is not yet available"
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

// New returns either the error itself if it's of type 'api.Error' or an 'Error' created from a normal string
func New(err error) error {
	if err == nil {
		return nil
	}
	var v *Error
	switch {
	case errors.As(err, &v):
		return v
	default:
		return &Error{Detail: err.Error()}
	}
}

// NewNonNil returns empty 'Error' if the incoming parameter is nil. This allows to perform the checks for
// error code without risks to get nil pointer
// Unfortunately we have to do this as we cannot return *Error directly in our method signatures
// (https://golang.org/doc/faq#nil_error)
func NewNonNil(err error) *Error {
	if err == nil {
		return &Error{}
	}
	return New(err).(*Error)
}

// NewErrorWithCode returns the Error initialized with the code passed. This is convenient for testing.
func NewErrorWithCode(code string) *Error {
	return &Error{ErrorCode: code}
}

// Error
func (e *Error) Error() string {
	if e.Status != nil || e.ErrorCode != "" {
		msg := ""
		if e.Status != nil {
			msg += fmt.Sprintf("Status: %d", *e.Status)
		}
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

// ErrorBackupDaemonConfigIsNotFound returns whether the api-error is of not found. Sometimes OM only returns the
// http code.
func (e *Error) ErrorBackupDaemonConfigIsNotFound() bool {
	if e == nil {
		return false
	}

	if e.Status != nil && *e.Status == 404 {
		return true
	}

	if e.ErrorCode == BackupDaemonConfigNotFound {
		return true
	}

	return false
}

// ErrorBackupVersionNotAvailable returns true if this is a 409 Conflict error indicating
// that MongoDB version information is not yet available in Ops Manager. This is a transient
// condition that occurs when the monitoring agent has not yet reported version information
// to Ops Manager, typically after a MongoDB deployment is created or recreated.
func (e *Error) ErrorBackupVersionNotAvailable() bool {
	if e == nil {
		return false
	}

	// This error is returned as a 409 Conflict with the message in the Detail field
	if e.Status != nil && *e.Status == 409 && strings.Contains(e.Detail, BackupVersionNotAvailable) {
		return true
	}

	return false
}
