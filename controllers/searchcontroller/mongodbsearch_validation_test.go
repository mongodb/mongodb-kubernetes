package searchcontroller

import (
	"testing"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/stretchr/testify/assert"
)

func TestValidateJVMFlags(t *testing.T) {
	testCases := []struct {
		name          string
		jvmFlags      []string
		expectError   bool
		errorContains string
	}{
		{
			name:        "Valid: -Xmx flag",
			jvmFlags:    []string{"-Xmx2g"},
			expectError: false,
		},
		{
			name:        "Valid: multiple jvm flags",
			jvmFlags:    []string{"-Xmx2g", "-Xms512m", "-XX:+UseG1GC"},
			expectError: false,
		},
		{
			name:        "Valid: -D system property",
			jvmFlags:    []string{"-Dsome.property=value"},
			expectError: false,
		},
		{
			name:        "Valid: -XX flag with numeric value",
			jvmFlags:    []string{"-XX:MaxGCPauseMillis=200"},
			expectError: false,
		},
		{
			name:        "Valid: use nil for jvm flags",
			jvmFlags:    nil,
			expectError: false,
		},
		{
			name:        "Valid: empty slice as jvm flags",
			jvmFlags:    []string{},
			expectError: false,
		},
		{
			name:          "Invalid: empty string as jvm flag",
			jvmFlags:      []string{""},
			expectError:   true,
			errorContains: "must not be empty",
		},
		{
			name:          "Invalid: jvm flag with space",
			jvmFlags:      []string{"-Xmx2g -Xms512m"},
			expectError:   true,
			errorContains: "must not contain spaces",
		},
		{
			name:          "Invalid: jvm flag with invalid prefix",
			jvmFlags:      []string{"-verbose:gc"},
			expectError:   true,
			errorContains: "must start with -X, -XX:, or -D",
		},
		{
			name:          "Invalid: jvm flag doesn't have dash prefix",
			jvmFlags:      []string{"Xmx2g"},
			expectError:   true,
			errorContains: "must start with -X, -XX:, or -D",
		},
		{
			name:          "Invalid: jvm flag has invalid characters",
			jvmFlags:      []string{"-Xmx2g;echo"},
			expectError:   true,
			errorContains: "contains invalid characters",
		},
		{
			name:          "Invalid: run another shell cmd (shell injection attempt) using flag",
			jvmFlags:      []string{"-Xmx2g$(whoami)"},
			expectError:   true,
			errorContains: "contains invalid characters",
		},
		{
			name:          "Invalid: second jvm flag invalid",
			jvmFlags:      []string{"-Xmx2g", "-invalid"},
			expectError:   true,
			errorContains: "must start with -X, -XX:, or -D",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.JVMFlags = tc.jvmFlags
			})

			err := search.ValidateSpec()
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
