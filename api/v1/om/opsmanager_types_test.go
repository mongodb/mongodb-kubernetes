package om

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
)

func TestMongoDBOpsManager_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDBOpsManager{}
	resource.AddOpsManagerWarningIfNotExists("my test warning")
	resource.AddOpsManagerWarningIfNotExists("my test warning")
	resource.AddOpsManagerWarningIfNotExists("my other test warning")
	assert.Equal(t, []status.Warning{"my test warning;", "my other test warning"}, resource.Status.OpsManagerStatus.Warnings)
	assert.Empty(t, resource.Status.AppDbStatus.Warnings)
	assert.Empty(t, resource.Status.BackupStatus.Warnings)
}

func TestAppDB_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDBOpsManager{}
	resource.AddAppDBWarningIfNotExists("my test warning")
	resource.AddAppDBWarningIfNotExists("my test warning")
	resource.AddAppDBWarningIfNotExists("my other test warning")
	assert.Equal(t, []status.Warning{"my test warning;", "my other test warning"}, resource.Status.AppDbStatus.Warnings)
	assert.Empty(t, resource.Status.BackupStatus.Warnings)
	assert.Empty(t, resource.Status.OpsManagerStatus.Warnings)
}

func TestBackup_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDBOpsManager{}
	resource.AddBackupWarningIfNotExists("my test warning")
	resource.AddBackupWarningIfNotExists("my test warning")
	resource.AddBackupWarningIfNotExists("my other test warning")
	assert.Equal(t, []status.Warning{"my test warning;", "my other test warning"}, resource.Status.BackupStatus.Warnings)
	assert.Empty(t, resource.Status.AppDbStatus.Warnings)
	assert.Empty(t, resource.Status.OpsManagerStatus.Warnings)
}

func TestGetPartsFromStatusOptions(t *testing.T) {
	t.Run("Empty list returns nil slice", func(t *testing.T) {
		assert.Nil(t, getPartsFromStatusOptions())
	})

	t.Run("Ops Manager parts are extracted correctly", func(t *testing.T) {
		statusOptions := []status.Option{
			status.NewBackupStatusOption("some-status"),
			status.NewOMPartOption(status.OpsManager),
			status.NewOMPartOption(status.Backup),
			status.NewOMPartOption(status.AppDb),
			status.NewBaseUrlOption("base-url"),
		}
		res := getPartsFromStatusOptions(statusOptions...)
		assert.Len(t, res, 3)
		assert.Equal(t, status.OpsManager, res[0])
		assert.Equal(t, status.Backup, res[1])
		assert.Equal(t, status.AppDb, res[2])
	})
}

func TestTLSCertificateSecretName(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.SetName("new-manager")
	tests := []struct {
		name     string
		security MongoDBOpsManagerSecurity
		expected string
	}{
		{
			name:     "TLS Certificate Secret name empty",
			security: MongoDBOpsManagerSecurity{},
			expected: "",
		},
		{
			name: "TLS Certificate Secret name from TLS.SecretRef.Name",
			security: MongoDBOpsManagerSecurity{
				TLS: MongoDBOpsManagerTLS{
					SecretRef: TLSSecretRef{
						Name: "ops-manager-cert",
					},
				},
			},
			expected: "ops-manager-cert",
		},
		{
			name: "TLS Certificate Secret name from Security.CertificatesSecretPrefix",
			security: MongoDBOpsManagerSecurity{
				CertificatesSecretsPrefix: "om",
			},
			expected: "om-new-manager-cert",
		},
		{
			name: "TLS Certificate Secret name from TLS.SecretRef.Name has priority",
			security: MongoDBOpsManagerSecurity{
				TLS: MongoDBOpsManagerTLS{
					SecretRef: TLSSecretRef{
						Name: "ops-manager-cert",
					},
				},
				CertificatesSecretsPrefix: "prefix",
			},
			expected: "ops-manager-cert",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om.Spec.Security = &tc.security
			assert.Equal(t, tc.expected, om.TLSCertificateSecretName())
		})
	}
}

func TestIsTLSEnabled(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	tests := []struct {
		name     string
		security *MongoDBOpsManagerSecurity
		expected bool
	}{
		{
			name:     "TLS is not enabled when security is not specified",
			security: nil,
			expected: false,
		},
		{
			name: "TLS is not enabled when TLS.SecretRef.Name is not specified",
			security: &MongoDBOpsManagerSecurity{
				TLS: MongoDBOpsManagerTLS{
					SecretRef: TLSSecretRef{},
				},
			},
			expected: false,
		},
		{
			name: "TLS is enabled when TLS.SecretRef.Name is specified",
			security: &MongoDBOpsManagerSecurity{
				TLS: MongoDBOpsManagerTLS{
					SecretRef: TLSSecretRef{
						Name: "ops-manager-cert",
					},
				},
			},
			expected: true,
		},
		{
			name: "TLS is enabled when CertificatesSecretsPrefix is specified",
			security: &MongoDBOpsManagerSecurity{
				CertificatesSecretsPrefix: "prefix",
			},
			expected: true,
		},
		{
			name: "TLS is enabled when both sources of cert secret name are specified",
			security: &MongoDBOpsManagerSecurity{
				TLS: MongoDBOpsManagerTLS{
					SecretRef: TLSSecretRef{
						Name: "ops-manager-cert",
					},
				},
				CertificatesSecretsPrefix: "prefix",
			},
			expected: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om.Spec.Security = tc.security
			assert.Equal(t, tc.expected, om.IsTLSEnabled())
		})
	}
}
