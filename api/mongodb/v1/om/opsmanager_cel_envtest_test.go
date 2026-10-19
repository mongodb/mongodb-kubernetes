package om_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_opsmanagers.yaml")))
}

func newOpsManager(t *testing.T, mutate func(*omv1.MongoDBOpsManager)) *omv1.MongoDBOpsManager {
	t.Helper()
	om := &omv1.MongoDBOpsManager{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "test-cel-", Namespace: "default"},
		Spec: omv1.MongoDBOpsManagerSpec{
			Version:  "8.0.19",
			Replicas: 1,
		},
	}
	if mutate != nil {
		mutate(om)
	}
	return om
}

func TestOpsManagerCELValidation_ObjectLockRetention(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	tests := []struct {
		name          string
		mutate        func(*omv1.MongoDBOpsManager)
		errorContains string
	}{
		{
			name: "objectRetentionDays without objectRetentionMode is rejected",
			mutate: func(om *omv1.MongoDBOpsManager) {
				om.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{Name: "test-om-db", Kind: "MongoDB"}
				om.Spec.Backup = &omv1.MongoDBOpsManagerBackup{
					Enabled:   true,
					S3Configs: []omv1.S3Config{{Name: "test", S3SecretRef: &omv1.SecretRef{Name: "test"}, ObjectLockEnabled: new(true), ObjectRetentionDays: new(30)}},
				}
			},
			errorContains: "'objectRetentionDays' and 'objectRetentionMode' must be specified together",
		},
		{
			name: "objectRetentionMode without objectRetentionDays is rejected",
			mutate: func(om *omv1.MongoDBOpsManager) {
				om.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{Name: "test-om-db", Kind: "MongoDB"}
				om.Spec.Backup = &omv1.MongoDBOpsManagerBackup{
					Enabled:   true,
					S3Configs: []omv1.S3Config{{Name: "test", S3SecretRef: &omv1.SecretRef{Name: "test"}, ObjectLockEnabled: new(true), ObjectRetentionMode: new("COMPLIANCE")}},
				}
			},
			errorContains: "'objectRetentionDays' and 'objectRetentionMode' must be specified together",
		},
		{
			name: "objectRetentionDays without objectLockEnabled is rejected",
			mutate: func(om *omv1.MongoDBOpsManager) {
				om.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{Name: "test-om-db", Kind: "MongoDB"}
				om.Spec.Backup = &omv1.MongoDBOpsManagerBackup{
					Enabled:   true,
					S3Configs: []omv1.S3Config{{Name: "test", S3SecretRef: &omv1.SecretRef{Name: "test"}, ObjectRetentionDays: new(30), ObjectRetentionMode: new("GOVERNANCE")}},
				}
			},
			errorContains: "'objectRetentionDays' and 'objectRetentionMode' require 'objectLockEnabled' to be enabled",
		},
		{
			name: "object lock retention fields on s3Stores with objectLockEnabled is accepted",
			mutate: func(om *omv1.MongoDBOpsManager) {
				om.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{Name: "test-om-db", Kind: "MongoDB"}
				om.Spec.Backup = &omv1.MongoDBOpsManagerBackup{
					Enabled:   true,
					S3Configs: []omv1.S3Config{{Name: "test", S3SecretRef: &omv1.SecretRef{Name: "test"}, ObjectLockEnabled: new(true), ObjectRetentionDays: new(30), ObjectRetentionMode: new("GOVERNANCE")}},
				}
			},
		},
		{
			name: "object lock retention fields on s3OpLogStores without objectLockEnabled is rejected",
			mutate: func(om *omv1.MongoDBOpsManager) {
				om.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{Name: "test-om-db", Kind: "MongoDB"}
				om.Spec.Backup = &omv1.MongoDBOpsManagerBackup{
					Enabled:             true,
					S3OplogStoreConfigs: []omv1.S3Config{{Name: "test", S3SecretRef: &omv1.SecretRef{Name: "test"}, ObjectRetentionDays: new(30), ObjectRetentionMode: new("GOVERNANCE")}},
				}
			},
			errorContains: "'objectRetentionDays' and 'objectRetentionMode' require 'objectLockEnabled' to be enabled",
		},
		{
			name: "object lock retention fields on s3OpLogStores with objectLockEnabled is accepted",
			mutate: func(om *omv1.MongoDBOpsManager) {
				om.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{Name: "test-om-db", Kind: "MongoDB"}
				om.Spec.Backup = &omv1.MongoDBOpsManagerBackup{
					Enabled:             true,
					S3OplogStoreConfigs: []omv1.S3Config{{Name: "test", S3SecretRef: &omv1.SecretRef{Name: "test"}, ObjectLockEnabled: new(true), ObjectRetentionDays: new(30), ObjectRetentionMode: new("GOVERNANCE")}},
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om := newOpsManager(t, tc.mutate)
			err := k8sClient.Create(ctx, om)

			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}

func TestOpsManagerCELValidation_AppDBOrExternalRefRequired(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	tests := []struct {
		name          string
		mutate        func(*omv1.MongoDBOpsManager)
		errorContains string
	}{
		{
			name: "neither applicationDatabase nor externalApplicationDatabaseRef is rejected",
			mutate: func(om *omv1.MongoDBOpsManager) {
				om.Spec.AppDB = nil
				om.Spec.ExternalAppDBRef = nil
			},
			errorContains: "at least one of spec.applicationDatabase or spec.externalApplicationDatabaseRef must be set",
		},
		{
			name: "applicationDatabase set is accepted",
			mutate: func(om *omv1.MongoDBOpsManager) {
				om.Spec.AppDB = &omv1.AppDBSpec{
					Version: "6.0.0",
					MonitoringAgent: mdbv1.MonitoringAgentConfig{
						StartupParameters: mdbv1.StartupParameters{},
					},
				}
			},
		},
		{
			name: "externalApplicationDatabaseRef set is accepted",
			mutate: func(om *omv1.MongoDBOpsManager) {
				om.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{
					Name: "test-om-db",
					Kind: "MongoDB",
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om := newOpsManager(t, tc.mutate)
			err := k8sClient.Create(ctx, om)

			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}

func TestOpsManagerCELValidation_ExternalAppDBTopologyTransition(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	tests := []struct {
		name             string
		initialAppDBMC   bool
		newKind          string
		expectedErrorMsg string
	}{
		{
			name:             "single-cluster internal AppDB rejects MongoDBMultiCluster external ref",
			newKind:          "MongoDBMultiCluster",
			expectedErrorMsg: "topology does not match between the current AppDB and the external AppDB",
		},
		{
			name:             "multi-cluster internal AppDB rejects MongoDB external ref",
			initialAppDBMC:   true,
			newKind:          "MongoDB",
			expectedErrorMsg: "topology does not match between the current AppDB and the external AppDB",
		},
		{
			name:    "single-cluster internal AppDB accepts MongoDB external ref",
			newKind: "MongoDB",
		},
		{
			name:           "multi-cluster internal AppDB accepts MongoDBMultiCluster external ref",
			initialAppDBMC: true,
			newKind:        "MongoDBMultiCluster",
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("cel-topology-%d", i)
			appDB := &omv1.AppDBSpec{
				Version:         "6.0.0",
				MonitoringAgent: mdbv1.MonitoringAgentConfig{StartupParameters: mdbv1.StartupParameters{}},
			}
			if tc.initialAppDBMC {
				appDB.Topology = omv1.ClusterTopologyMultiCluster
				appDB.ClusterSpecList = mdbv1.ClusterSpecList{
					{ClusterName: "cluster-1", Members: 3},
					{ClusterName: "cluster-2", Members: 3},
				}
			}
			om := &omv1.MongoDBOpsManager{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: omv1.MongoDBOpsManagerSpec{
					Version:  "8.0.19",
					Replicas: 1,
					AppDB:    appDB,
				},
			}

			require.NoError(t, k8sClient.Create(ctx, om))
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, om) })

			om.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{Name: name + "-db", Kind: tc.newKind}
			err := k8sClient.Update(ctx, om)

			if tc.expectedErrorMsg == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.expectedErrorMsg)
		})
	}
}

func TestOpsManagerCELValidation_ExternalRefKindEnum(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	tests := []struct {
		name          string
		kind          string
		errorContains string
	}{
		{
			name: "om-mongodb",
			kind: "MongoDB",
		},
		{
			name: "om-multicluster",
			kind: "MongoDBMultiCluster",
		},
		{
			name:          "om-bogus",
			kind:          "Bogus",
			errorContains: `Unsupported value: "Bogus": supported values: "MongoDB", "MongoDBMultiCluster"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om := omv1.NewOpsManagerBuilder().
				SetName(tc.name).
				SetNamespace("default").
				SetVersion("8.0.25").
				SetAppDBToNil().
				SetExternalAppDBRef(omv1.ExternalAppDBRef{Name: "om-db", Kind: tc.kind}).
				Build()

			err := k8sClient.Create(ctx, om)

			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}

func TestOpsManagerCELValidation_LicenseReleaseReceipts(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client
	released := func(id string) omv1.LicenseReleaseReceipt {
		return omv1.LicenseReleaseReceipt{ArtifactID: id}
	}

	tests := []struct {
		name          string
		license       *omv1.MongoDBOpsManagerLicense
		errorContains string
	}{
		{
			name: "entries naming the artifact by id are accepted",
			license: &omv1.MongoDBOpsManagerLicense{
				LicenseSecretRef: &omv1.SecretRef{Name: "om-license"},
				ReleaseReceipts:  []omv1.LicenseReleaseReceipt{released("art-1"), released("art-2")},
			},
		},
		{
			name: "entry naming the artifact by its old licence Secret is accepted",
			license: &omv1.MongoDBOpsManagerLicense{
				ReleaseReceipts: []omv1.LicenseReleaseReceipt{{
					LicenseSecretRef: &omv1.SecretRef{Name: "old-license"},
				}},
			},
		},
		{
			name: "entry with both artifactId and licenseSecretRef is rejected",
			license: &omv1.MongoDBOpsManagerLicense{
				ReleaseReceipts: []omv1.LicenseReleaseReceipt{{
					ArtifactID:       "art-1",
					LicenseSecretRef: &omv1.SecretRef{Name: "old-license"},
				}},
			},
			errorContains: "exactly one of artifactId or licenseSecretRef must be set",
		},
		{
			name: "entry with neither artifactId nor licenseSecretRef is rejected",
			license: &omv1.MongoDBOpsManagerLicense{
				ReleaseReceipts: []omv1.LicenseReleaseReceipt{{}},
			},
			errorContains: "exactly one of artifactId or licenseSecretRef must be set",
		},
		{
			name: "entry with an empty licenseSecretRef name is rejected",
			license: &omv1.MongoDBOpsManagerLicense{
				ReleaseReceipts: []omv1.LicenseReleaseReceipt{{LicenseSecretRef: &omv1.SecretRef{}}},
			},
			errorContains: "licenseSecretRef.name must not be empty",
		},
		{
			name: "same artifactId listed twice is rejected",
			license: &omv1.MongoDBOpsManagerLicense{
				ReleaseReceipts: []omv1.LicenseReleaseReceipt{released("art-1"), released("art-1")},
			},
			errorContains: "releaseReceipts must not list the same artifactId twice",
		},
		{
			name: "same licenseSecretRef listed twice is rejected",
			license: &omv1.MongoDBOpsManagerLicense{
				ReleaseReceipts: []omv1.LicenseReleaseReceipt{
					{LicenseSecretRef: &omv1.SecretRef{Name: "old-license"}},
					{LicenseSecretRef: &omv1.SecretRef{Name: "old-license"}},
				},
			},
			errorContains: "releaseReceipts must not list the same licenseSecretRef twice",
		},
		{
			name: "licenseSecretRef also listed in releaseReceipts is rejected",
			license: &omv1.MongoDBOpsManagerLicense{
				LicenseSecretRef: &omv1.SecretRef{Name: "om-license"},
				ReleaseReceipts: []omv1.LicenseReleaseReceipt{
					{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}},
				},
			},
			errorContains: "licenseSecretRef must not also be listed in releaseReceipts",
		},
		{
			name: "distinct Secrets and artifact ids are accepted",
			license: &omv1.MongoDBOpsManagerLicense{
				LicenseSecretRef: &omv1.SecretRef{Name: "om-license"},
				ReleaseReceipts: []omv1.LicenseReleaseReceipt{
					released("art-1"),
					{LicenseSecretRef: &omv1.SecretRef{Name: "old-license-1"}},
					{LicenseSecretRef: &omv1.SecretRef{Name: "old-license-2"}},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om := newOpsManager(t, func(om *omv1.MongoDBOpsManager) {
				om.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{Name: "test-om-db", Kind: "MongoDB"}
				om.Spec.License = tc.license
			})
			err := k8sClient.Create(ctx, om)
			if tc.errorContains == "" {
				require.NoError(t, err)
				_ = k8sClient.Delete(ctx, om)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}
