package apierrors

import "strings"

// objectModifiedText is an error indicating that we are trying to update a resource that has since been updated.
// in this case we just want to retry but not log it as an error.
var objectModifiedText = "the object has been modified; please apply your changes to the latest version and try again"

// automationConfigConflictText is the Ops Manager error code for a publish that lost a race
// with another writer. The next reconcile re-reads the config, so the error is transient.
var automationConfigConflictText = "automation_config_concurrent_modification"

var transientTexts = []string{objectModifiedText, automationConfigConflictText}

// IsTransientError returns a boolean indicating if a given error is transient.
func IsTransientError(err error) bool {
	return IsTransientMessage(err.Error())
}

// IsTransientMessage returns a boolean indicating if a given error message is transient.
func IsTransientMessage(msg string) bool {
	lower := strings.ToLower(msg)
	for _, text := range transientTexts {
		if strings.Contains(lower, text) {
			return true
		}
	}
	return false
}
