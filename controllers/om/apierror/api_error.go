package apierror

import (
	"errors"
	"fmt"
)

const (
	// Error codes that Ops Manager may return that we are concerned about
	OrganizationNotFound       = "ORG_NAME_NOT_FOUND"
	ProjectNotFound            = "GROUP_NAME_NOT_FOUND"
	BackupDaemonConfigNotFound = "DAEMON_MACHINE_CONFIG_NOT_FOUND"
	UserAlreadyExists          = "USER_ALREADY_EXISTS"
	DuplicateWhitelistEntry    = "DUPLICATE_GLOBAL_WHITELIST_ENTRY"
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

// IsRetryableError returns true if the error is retryable. For API errors, this
// means the status is nil (network error wrapped as api.Error), 429 (rate limited),
// or 500-599 (server error) except 501 (Not Implemented). For non-API errors
// (network, DNS, timeout, etc.) we default to retryable.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.isRetryable()
	}
	// Non-API errors (network failures, DNS, timeouts) are retryable by default
	return true
}

// isRetryable returns true if the API error represents a transient/retryable condition.
func (e *Error) isRetryable() bool {
	if e == nil {
		return false
	}
	// No HTTP status means this is a wrapped network/transport error
	if e.Status == nil {
		return true
	}
	status := *e.Status
	// 429 Too Many Requests
	if status == 429 {
		return true
	}
	// 5xx server errors except 501 Not Implemented
	if status >= 500 && status <= 599 && status != 501 {
		return true
	}
	return false
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
