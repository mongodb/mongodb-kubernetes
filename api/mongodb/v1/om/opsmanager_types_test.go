package om

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
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

func TestOpsManagerManagedCertificateNames(t *testing.T) {
	managed := &ManagedCertificate{}

	tests := []struct {
		name                 string
		security             *MongoDBOpsManagerSecurity
		backup               *MongoDBOpsManagerBackup
		wantTLS              bool
		wantManaged          bool
		wantBackupManaged    bool
		wantCertSecret       string
		wantCA               string
		wantBackupPEMSecret  string
		wantBackupCertSecret string
	}{
		{
			name:                 "nil security and backup are safe",
			wantCertSecret:       "",
			wantCA:               "",
			wantBackupPEMSecret:  "",
			wantBackupCertSecret: "",
		},
		{
			name:           "managed OM certificate enables TLS",
			security:       &MongoDBOpsManagerSecurity{ManagedCertificate: managed},
			wantTLS:        true,
			wantManaged:    true,
			wantCertSecret: "example-managed-om-cert",
			wantCA:         "example-managed-om-ca-bundle",
		},
		{
			name: "legacy TLS secret ref keeps precedence when managed mode is absent",
			security: &MongoDBOpsManagerSecurity{
				TLS: MongoDBOpsManagerTLS{SecretRef: TLSSecretRef{Name: "custom-om-cert"}},
			},
			wantTLS:        true,
			wantCertSecret: "custom-om-cert",
		},
		{
			name: "legacy certificates prefix keeps precedence when managed mode is absent",
			security: &MongoDBOpsManagerSecurity{
				CertificatesSecretsPrefix: "prefix",
			},
			wantTLS:        true,
			wantCertSecret: "prefix-example-cert",
		},
		{
			name: "legacy CA is resolved from the spec when managed mode is absent",
			security: &MongoDBOpsManagerSecurity{
				TLS: MongoDBOpsManagerTLS{CA: "custom-om-ca"},
			},
			wantCA: "custom-om-ca",
		},
		{
			name:                 "managed backup certificate activates independently",
			backup:               &MongoDBOpsManagerBackup{Enabled: true, ManagedCertificate: managed},
			wantBackupManaged:    true,
			wantBackupPEMSecret:  "example-managed-backup-pem",
			wantBackupCertSecret: "example-managed-backup-cert",
		},
		{
			name:                 "legacy queryable backup secret is kept when managed mode is absent",
			backup:               &MongoDBOpsManagerBackup{Enabled: true, QueryableBackupSecretRef: SecretRef{Name: "custom-queryable"}},
			wantBackupPEMSecret:  "custom-queryable",
			wantBackupCertSecret: "",
		},
		{
			name:                 "managed OM and backup certificates activate independently",
			security:             &MongoDBOpsManagerSecurity{ManagedCertificate: managed},
			backup:               &MongoDBOpsManagerBackup{Enabled: true, ManagedCertificate: managed},
			wantTLS:              true,
			wantManaged:          true,
			wantBackupManaged:    true,
			wantCertSecret:       "example-managed-om-cert",
			wantCA:               "example-managed-om-ca-bundle",
			wantBackupPEMSecret:  "example-managed-backup-pem",
			wantBackupCertSecret: "example-managed-backup-cert",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om := &MongoDBOpsManager{
				ObjectMeta: metav1.ObjectMeta{Name: "example"},
				Spec: MongoDBOpsManagerSpec{
					Security: tc.security,
					Backup:   tc.backup,
				},
			}

			assert.Equal(t, tc.wantTLS, om.IsTLSEnabled())
			assert.Equal(t, tc.wantManaged, om.IsManagedCertificateEnabled())
			assert.Equal(t, tc.wantBackupManaged, om.IsBackupManagedCertificateEnabled())
			assert.Equal(t, tc.wantCertSecret, om.TLSCertificateSecretName())
			assert.Equal(t, tc.wantCA, om.GetOpsManagerCA())
			assert.Equal(t, tc.wantBackupPEMSecret, om.QueryableBackupTLSSecretName())
			assert.Equal(t, tc.wantBackupCertSecret, om.QueryableBackupCertificateSecretName())
		})
	}
}

func TestOpsManagerManagedCertificateConditions(t *testing.T) {
	om := &MongoDBOpsManager{ObjectMeta: metav1.ObjectMeta{Name: "example", Generation: 3}}
	first := metav1.NewTime(time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC))

	om.SetStatusCondition(metav1.Condition{
		Type:               "CertificatesReady",
		Status:             metav1.ConditionFalse,
		Reason:             "Issuing",
		Message:            "issuing certificates",
		LastTransitionTime: first,
	})
	require.Len(t, om.Status.Conditions, 1)
	condition := om.Status.Conditions[0]
	assert.Equal(t, int64(3), condition.ObservedGeneration)
	assert.Equal(t, first, condition.LastTransitionTime)

	om.SetGeneration(4)
	om.SetStatusCondition(metav1.Condition{
		Type:               "CertificatesReady",
		Status:             metav1.ConditionFalse,
		Reason:             "Issuing",
		Message:            "still issuing certificates",
		LastTransitionTime: metav1.NewTime(first.Add(time.Hour)),
	})
	require.Len(t, om.Status.Conditions, 1)
	condition = om.Status.Conditions[0]
	assert.Equal(t, first, condition.LastTransitionTime, "unchanged status must keep the transition time")
	assert.Equal(t, "still issuing certificates", condition.Message)
	assert.Equal(t, int64(4), condition.ObservedGeneration)

	ready := metav1.NewTime(first.Add(2 * time.Hour))
	om.SetStatusCondition(metav1.Condition{
		Type:               "CertificatesReady",
		Status:             metav1.ConditionTrue,
		Reason:             "AllCertificatesReady",
		Message:            "all certificates are ready",
		LastTransitionTime: ready,
	})
	require.Len(t, om.Status.Conditions, 1)
	condition = om.Status.Conditions[0]
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
	assert.Equal(t, ready, condition.LastTransitionTime)
	assert.Equal(t, int64(4), condition.ObservedGeneration)
}

func TestManagedCertificateEmbedsSharedConfig(t *testing.T) {
	tests := []struct {
		name           string
		certificate    *ManagedCertificate
		wantIssuerName string
		wantSubjectCN  string
	}{
		{
			name:        "empty block uses operator-managed CA",
			certificate: &ManagedCertificate{},
		},
		{
			name: "issuer, CA and subject are accepted",
			certificate: &ManagedCertificate{
				CertificateConfig: mdbv1.CertificateConfig{
					IssuerRef: &mdbv1.IssuerRef{Name: "custom-issuer"},
					CA:        &mdbv1.CARef{Name: "custom-ca", Kind: "ConfigMap", Key: "ca.crt"},
				},
				Subject: &mdbv1.X509Subject{CommonName: "om.example.com"},
			},
			wantIssuerName: "custom-issuer",
			wantSubjectCN:  "om.example.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.certificate.IssuerRef != nil {
				assert.Equal(t, tc.wantIssuerName, tc.certificate.IssuerRef.Name)
			}
			if tc.certificate.Subject != nil {
				assert.Equal(t, tc.wantSubjectCN, tc.certificate.Subject.CommonName)
			}
		})
	}
}

func TestAppDBName(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(om *MongoDBOpsManager)
		expected string
	}{
		{
			name: "returns externalApplicationDatabaseRef name when set",
			prepare: func(om *MongoDBOpsManager) {
				om.Spec.AppDB = nil
				om.Spec.ExternalAppDBRef = &ExternalAppDBRef{Name: "external-appdb", Kind: "MongoDB"}
			},
			expected: "external-appdb",
		},
		{
			name:     "returns AppDB name when externalApplicationDatabaseRef is not set",
			expected: "om-test-db",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om := NewOpsManagerBuilderDefault().SetName("om-test").Build()
			if tc.prepare != nil {
				tc.prepare(om)
			}
			assert.Equal(t, tc.expected, om.AppDBName())
		})
	}
}

func TestOpsManager_InitDefaultFields_ExternalRefSkipsAppDBDefaulting(t *testing.T) {
	externalRef := &ExternalAppDBRef{Name: "om-test-db", Kind: "MongoDB"}
	tests := []struct {
		name             string
		spec             MongoDBOpsManagerSpec
		expectedAppDBNil bool
		expectedDefault  bool
	}{
		{
			name:             "ref-only: AppDB stays nil",
			spec:             MongoDBOpsManagerSpec{ExternalAppDBRef: externalRef},
			expectedAppDBNil: true,
		},
		{
			name:            "both set: AppDB kept but not defaulted",
			spec:            MongoDBOpsManagerSpec{AppDB: &AppDBSpec{}, ExternalAppDBRef: externalRef},
			expectedDefault: false,
		},
		{
			name:            "appdb-only: defaulted as today",
			spec:            MongoDBOpsManagerSpec{AppDB: &AppDBSpec{}},
			expectedDefault: true,
		},
		{
			name:            "neither set: instantiated as today",
			spec:            MongoDBOpsManagerSpec{},
			expectedDefault: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om := &MongoDBOpsManager{
				ObjectMeta: metav1.ObjectMeta{Name: "om-test", Namespace: "mongodb"},
				Spec:       tc.spec,
			}

			om.InitDefaultFields()

			if tc.expectedAppDBNil {
				assert.Nil(t, om.Spec.AppDB)
				return
			}
			require.NotNil(t, om.Spec.AppDB)
			if tc.expectedDefault {
				assert.Equal(t, "om-test", om.Spec.AppDB.OpsManagerName)
				assert.Equal(t, "mongodb", om.Spec.AppDB.Namespace)
				assert.NotNil(t, om.Spec.AppDB.Security)
			} else {
				assert.Equal(t, "", om.Spec.AppDB.OpsManagerName)
				assert.Nil(t, om.Spec.AppDB.Security)
			}
		})
	}
}

func TestOpsManager_UnmarshalJSON_ExternalRefLeavesAppDBNil(t *testing.T) {
	data := []byte(`{"metadata":{"name":"om-test","namespace":"mongodb"},"spec":{"externalApplicationDatabaseRef":{"name":"om-test-db","kind":"MongoDB"}}}`)
	om := &MongoDBOpsManager{}
	require.NoError(t, json.Unmarshal(data, om))
	assert.NotNil(t, om.Spec.ExternalAppDBRef)
	assert.Nil(t, om.Spec.AppDB)
}

func TestUpdateStatusAppDb_ExternalAppDBRefResetsStatus(t *testing.T) {
	t.Run("external ref set + nil AppDB resets AppDbStatus", func(t *testing.T) {
		resource := &MongoDBOpsManager{
			Spec: MongoDBOpsManagerSpec{
				ExternalAppDBRef: &ExternalAppDBRef{
					Name: "test-om-db",
					Kind: "MongoDB",
				},
			},
		}
		resource.Status.AppDbStatus.Members = 3
		resource.Status.AppDbStatus.Version = "4.4.20"
		resource.Status.AppDbStatus.FeatureCompatibilityVersion = "4.4"
		resource.Status.AppDbStatus.ClusterStatusList = []status.ClusterStatusItem{{ClusterName: "cluster-1"}}
		resource.Status.AppDbStatus.Warnings = []status.Warning{"stale warning;"}

		require.NotPanics(t, func() {
			resource.UpdateStatus(status.PhaseDisabled, status.NewOMPartOption(status.AppDb))
		})

		assert.Equal(t, status.PhaseDisabled, resource.Status.AppDbStatus.Phase)
		assert.Empty(t, resource.Status.AppDbStatus.Members)
		assert.Empty(t, resource.Status.AppDbStatus.Version)
		assert.Empty(t, resource.Status.AppDbStatus.FeatureCompatibilityVersion)
		assert.Empty(t, resource.Status.AppDbStatus.ClusterStatusList)
		assert.Empty(t, resource.Status.AppDbStatus.Warnings)
	})

	t.Run("internal AppDB does not reset AppDbStatus", func(t *testing.T) {
		resource := &MongoDBOpsManager{
			Spec: MongoDBOpsManagerSpec{
				AppDB: &AppDBSpec{},
			},
		}
		resource.Status.AppDbStatus.Members = 3

		resource.UpdateStatus(status.PhaseRunning, status.NewOMPartOption(status.AppDb))

		assert.Equal(t, status.PhaseRunning, resource.Status.AppDbStatus.Phase)
		assert.Equal(t, 3, resource.Status.AppDbStatus.Members)
	})
}
