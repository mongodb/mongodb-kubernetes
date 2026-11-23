package om

import (
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
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
		wantBackupCertInUse  bool
		wantQueryableTLS     bool
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
			wantBackupCertInUse:  true,
			wantQueryableTLS:     true,
			wantBackupPEMSecret:  "example-managed-backup-pem",
			wantBackupCertSecret: "example-managed-backup-cert",
		},
		{
			name:                 "legacy queryable backup secret is kept when managed mode is absent",
			backup:               &MongoDBOpsManagerBackup{Enabled: true, QueryableBackupSecretRef: SecretRef{Name: "custom-queryable"}},
			wantQueryableTLS:     true,
			wantBackupPEMSecret:  "custom-queryable",
			wantBackupCertSecret: "",
		},
		{
			name:                 "managed backup certificate is inert while backup is disabled",
			backup:               &MongoDBOpsManagerBackup{ManagedCertificate: managed},
			wantBackupManaged:    true,
			wantBackupPEMSecret:  "example-managed-backup-pem",
			wantBackupCertSecret: "example-managed-backup-cert",
		},
		{
			name:                 "legacy queryable backup secret is wired while backup is disabled",
			backup:               &MongoDBOpsManagerBackup{QueryableBackupSecretRef: SecretRef{Name: "custom-queryable"}},
			wantQueryableTLS:     true,
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
			wantBackupCertInUse:  true,
			wantQueryableTLS:     true,
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
			assert.Equal(t, tc.wantBackupCertInUse, om.IsBackupManagedCertificateInUse())
			assert.Equal(t, tc.wantQueryableTLS, om.IsQueryableBackupTLSConfigured())
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

func TestOpsManagerGetLastSpec(t *testing.T) {
	legacySpec := func(t *testing.T, tlsEnabled bool) MongoDBOpsManagerSpec {
		t.Helper()
		security := &mdbv1.Security{CertificatesSecretsPrefix: "appdb"}
		if tlsEnabled {
			security.TLSConfig = &mdbv1.TLSConfig{Enabled: true}
		}
		return MongoDBOpsManagerSpec{
			Version:  "8.0.26",
			Replicas: 1,
			AppDB: &AppDBSpec{
				Version:  "8.0.0",
				Members:  3,
				Security: security,
			},
		}
	}

	managedSpec := func() MongoDBOpsManagerSpec {
		return MongoDBOpsManagerSpec{
			Version:  "8.0.26",
			Replicas: 1,
			AppDB: &AppDBSpec{
				Version: "8.0.0",
				Members: 3,
				Security: &mdbv1.Security{
					ManagedCertificate: &mdbv1.ManagedCertificate{},
				},
			},
		}
	}

	newOpsManager := func(annotations map[string]string, spec MongoDBOpsManagerSpec) *MongoDBOpsManager {
		return &MongoDBOpsManager{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-om",
				Namespace:   "test-ns",
				Annotations: annotations,
			},
			Spec: spec,
		}
	}

	serializedLegacy := func(t *testing.T, tlsEnabled bool) string {
		t.Helper()
		bytes, err := json.Marshal(legacySpec(t, tlsEnabled))
		require.NoError(t, err)
		return string(bytes)
	}

	tests := []struct {
		name        string
		opsManager  *MongoDBOpsManager
		expectedNil bool
		expectedErr bool
	}{
		{
			name:        "absent annotation decodes to nil",
			opsManager:  newOpsManager(nil, managedSpec()),
			expectedNil: true,
		},
		{
			name:        "empty annotation decodes to nil",
			opsManager:  newOpsManager(map[string]string{util.LastAchievedSpec: ""}, managedSpec()),
			expectedNil: true,
		},
		{
			name:        "malformed annotation is an error",
			opsManager:  newOpsManager(map[string]string{util.LastAchievedSpec: "{not json"}, managedSpec()),
			expectedErr: true,
		},
		{
			name: "nil AppDB round trip does not invent an AppDB",
			opsManager: newOpsManager(
				map[string]string{util.LastAchievedSpec: mustMarshal(t, MongoDBOpsManagerSpec{Version: "8.0.26", Replicas: 1})},
				managedSpec(),
			),
		},
		{
			name: "legacy prefix with tls.enabled round trip",
			opsManager: newOpsManager(
				map[string]string{util.LastAchievedSpec: serializedLegacy(t, true)},
				managedSpec(),
			),
		},
		{
			name: "legacy prefix-only AppDB TLS round trip",
			opsManager: newOpsManager(
				map[string]string{util.LastAchievedSpec: serializedLegacy(t, false)},
				managedSpec(),
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			specBefore := test.opsManager.Spec.DeepCopy()
			annotationsBefore := maps.Clone(test.opsManager.Annotations)

			last, err := test.opsManager.GetLastSpec()

			if test.expectedErr {
				require.Error(t, err)
				require.Nil(t, last)
				return
			}
			require.NoError(t, err)
			if test.expectedNil {
				require.Nil(t, last)
				return
			}
			require.NotNil(t, last)

			// The accessor must not mutate the live spec or the annotations.
			require.Equal(t, specBefore, &test.opsManager.Spec)
			require.Equal(t, annotationsBefore, test.opsManager.Annotations)

			if test.name == "nil AppDB round trip does not invent an AppDB" {
				require.Nil(t, last.AppDB)
				return
			}

			require.NotNil(t, last.AppDB)
			require.Equal(t, test.opsManager.Name, last.AppDB.OpsManagerName)
			require.Equal(t, test.opsManager.Namespace, last.AppDB.Namespace)
			require.Equal(t, "test-om-db", last.AppDB.Name())
			require.Equal(t, "appdb-test-om-db-cert", last.AppDB.GetSecurity().MemberCertificateSecretName(last.AppDB.Name()))
			require.True(
				t,
				mdbv1.IsMigratingToManagedCertificate(last.AppDB.GetSecurity(), test.opsManager.Spec.AppDB.GetSecurity()),
			)
		})
	}
}

func mustMarshal(t *testing.T, value any) string {
	t.Helper()
	bytes, err := json.Marshal(value)
	require.NoError(t, err)
	return string(bytes)
}
