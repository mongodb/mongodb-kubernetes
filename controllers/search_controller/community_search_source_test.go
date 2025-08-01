package search_controller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
)

func newCommunitySearchSource(version string, authModes []mdbcv1.AuthMode) *CommunitySearchSource {
	return &CommunitySearchSource{
		MongoDBCommunity: &mdbcv1.MongoDBCommunity{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-mongodb",
				Namespace: "test-namespace",
			},
			Spec: mdbcv1.MongoDBCommunitySpec{
				Version: version,
				Security: mdbcv1.Security{
					Authentication: mdbcv1.Authentication{
						Modes: authModes,
					},
				},
			},
		},
	}
}

func TestCommunitySearchSource_Validate(t *testing.T) {
	cases := []struct {
		name           string
		version        string
		authModes      []mdbcv1.AuthMode
		expectError    bool
		expectedErrMsg string
	}{
		// Version validation tests
		{
			name:           "Invalid version",
			version:        "invalid.version",
			authModes:      []mdbcv1.AuthMode{"SCRAM-SHA-256"},
			expectError:    true,
			expectedErrMsg: "error parsing MongoDB version",
		},
		{
			name:           "Version too old",
			version:        "7.0.0",
			authModes:      []mdbcv1.AuthMode{"SCRAM-SHA-256"},
			expectError:    true,
			expectedErrMsg: "MongoDB version must be 8.0.10 or higher",
		},
		{
			name:           "Version just below minimum",
			version:        "8.0.9",
			authModes:      []mdbcv1.AuthMode{"SCRAM-SHA-256"},
			expectError:    true,
			expectedErrMsg: "MongoDB version must be 8.0.10 or higher",
		},
		{
			name:        "Valid minimum version",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"SCRAM-SHA-256"},
			expectError: false,
		},
		{
			name:        "Version above minimum",
			version:     "8.1.0",
			authModes:   []mdbcv1.AuthMode{"SCRAM-SHA-256"},
			expectError: false,
		},
		{
			name:        "Version with build number",
			version:     "8.1.0-rc1",
			authModes:   []mdbcv1.AuthMode{"SCRAM-SHA-256"},
			expectError: false,
		},
		// Authentication mode tests - empty/nil cases
		{
			name:        "Empty auth modes",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{},
			expectError: false,
		},
		{
			name:        "Nil auth modes",
			version:     "8.0.10",
			authModes:   nil,
			expectError: false,
		},
		{
			name:           "X509 mode only",
			version:        "8.0.10",
			authModes:      []mdbcv1.AuthMode{"X509"},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch requires SCRAM authentication to be enabled",
		},
		{
			name:        "X509 and SCRAM",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"X509", "SCRAM-SHA-256"},
			expectError: false,
		},
		{
			name:        "Multiple auth modes with SCRAM first",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"SCRAM-SHA-1", "X509"},
			expectError: false,
		},
		{
			name:        "Multiple auth modes with SCRAM last",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"PLAIN", "X509", "SCRAM-SHA-256"},
			expectError: false,
		},
		{
			name:           "Multiple non-SCRAM modes",
			version:        "8.0.10",
			authModes:      []mdbcv1.AuthMode{"PLAIN", "X509"},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch requires SCRAM authentication to be enabled",
		},
		// SCRAM variant tests
		{
			name:        "SCRAM only",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"SCRAM"},
			expectError: false,
		},
		{
			name:        "SCRAM-SHA-1 only",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"SCRAM-SHA-1"},
			expectError: false,
		},
		{
			name:        "SCRAM-SHA-256 only",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"SCRAM-SHA-256"},
			expectError: false,
		},
		{
			name:        "All SCRAM variants",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"SCRAM", "SCRAM-SHA-1", "SCRAM-SHA-256"},
			expectError: false,
		},
		// Case-insensitive tests (now supported with ToUpper)
		{
			name:        "Lowercase SCRAM",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"scram-sha-256"},
			expectError: false,
		},
		{
			name:        "Mixed case SCRAM",
			version:     "8.0.10",
			authModes:   []mdbcv1.AuthMode{"Scram-Sha-256"},
			expectError: false,
		},
		// Edge case tests
		{
			name:           "PLAIN only",
			version:        "8.0.10",
			authModes:      []mdbcv1.AuthMode{"PLAIN"},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch requires SCRAM authentication to be enabled",
		},
		// Combined validation tests
		{
			name:           "Invalid version with valid auth",
			version:        "7.0.0",
			authModes:      []mdbcv1.AuthMode{"SCRAM-SHA-256"},
			expectError:    true,
			expectedErrMsg: "MongoDB version must be 8.0.10 or higher",
		},
		{
			name:           "Valid version with invalid auth",
			version:        "8.0.10",
			authModes:      []mdbcv1.AuthMode{"X509"},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch requires SCRAM authentication to be enabled",
		},
		{
			name:           "Invalid version with invalid auth",
			version:        "7.0.0",
			authModes:      []mdbcv1.AuthMode{"X509"},
			expectError:    true,
			expectedErrMsg: "MongoDB version must be 8.0.10 or higher", // Should fail on version first
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newCommunitySearchSource(c.version, c.authModes)
			err := src.Validate()

			if c.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), c.expectedErrMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
