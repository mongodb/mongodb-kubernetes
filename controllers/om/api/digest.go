package api

import (
	"encoding/json"
	"fmt"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
)

// parseAPIError
func parseAPIError(statusCode int, method, url string, body []byte) *apierror.Error {
	// If nobody - returning the error object with only HTTP status
	if body == nil {
		return &apierror.Error{
			Status: &statusCode,
			Detail: fmt.Sprintf("%s %v failed with status %d with no response body", method, url, statusCode),
		}
	}
	// If response body exists - trying to parse it
	errorObject := &apierror.Error{}
	if err := json.Unmarshal(body, errorObject); err != nil {
		// If parsing has failed - returning just the general error with status code
		return &apierror.Error{
			Status: &statusCode,
			Detail: fmt.Sprintf("%s %v failed with status %d with response body: %s", method, url, statusCode, string(body)),
		}
	}

	return errorObject
}
