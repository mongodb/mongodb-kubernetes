package om_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m,
		env.WithCRDs("mongodb.com_opsmanagers.yaml"),
		env.WithWebhooks("webhooks.yaml"),
	))
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

// newOpsManagerUnstructured returns a valid baseline object. A typed OM cannot express
// a missing required field: its struct tags always serialize the zero value.
func newOpsManagerUnstructured() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "mongodb.com/v1",
		"kind":       "MongoDBOpsManager",
		"metadata":   map[string]any{"generateName": "test-schema-", "namespace": "default"},
		"spec": map[string]any{
			"version":  "8.0.19",
			"replicas": int64(1),
			"applicationDatabase": map[string]any{
				"version": "6.0.5",
				"members": int64(3),
			},
			"backup": map[string]any{
				"enabled": true,
				"opLogStores": []any{
					map[string]any{"name": "oplog", "mongodbResourceRef": map[string]any{"name": "appdb"}},
				},
				"s3Stores": []any{
					map[string]any{
						"name":                   "s3",
						"pathStyleAccessEnabled": true,
						"s3BucketEndpoint":       "http://minio:9000",
						"s3BucketName":           "backups",
					},
				},
			},
			"externalConnectivity": map[string]any{"type": "LoadBalancer"},
		},
	}}
}

func withoutField(path ...string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) { unstructured.RemoveNestedField(u.Object, path...) }
}

// withoutStoreField rewrites the whole store list: RemoveNestedField does not
// descend into slices.
func withoutStoreField(store, field string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		stores, _, _ := unstructured.NestedSlice(u.Object, "spec", "backup", store)
		delete(stores[0].(map[string]any), field)
		_ = unstructured.SetNestedSlice(u.Object, stores, "spec", "backup", store)
	}
}

func withField(value any, path ...string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) { _ = unstructured.SetNestedField(u.Object, value, path...) }
}

func TestOpsManagerCRDSchemaValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	tests := []struct {
		name          string
		mutate        func(*unstructured.Unstructured)
		errorContains string
	}{
		{
			name: "valid baseline is accepted",
		},
		{
			name:          "applicationDatabase members below the minimum is rejected",
			mutate:        withField(int64(2), "spec", "applicationDatabase", "members"),
			errorContains: "spec.applicationDatabase.members in body should be greater than or equal to 3",
		},
		{
			name:          "backup without enabled is rejected",
			mutate:        withoutField("spec", "backup", "enabled"),
			errorContains: "spec.backup.enabled: Required value",
		},
		{
			name:          "opLogStores entry without name is rejected",
			mutate:        withoutStoreField("opLogStores", "name"),
			errorContains: "spec.backup.opLogStores[0].name: Required value",
		},
		{
			name:          "opLogStores entry without mongodbResourceRef is rejected",
			mutate:        withoutStoreField("opLogStores", "mongodbResourceRef"),
			errorContains: "spec.backup.opLogStores[0].mongodbResourceRef: Required value",
		},
		{
			name:          "s3Stores entry without name is rejected",
			mutate:        withoutStoreField("s3Stores", "name"),
			errorContains: "spec.backup.s3Stores[0].name: Required value",
		},
		{
			name:          "s3Stores entry without pathStyleAccessEnabled is rejected",
			mutate:        withoutStoreField("s3Stores", "pathStyleAccessEnabled"),
			errorContains: "spec.backup.s3Stores[0].pathStyleAccessEnabled: Required value",
		},
		{
			name:          "s3Stores entry without s3BucketEndpoint is rejected",
			mutate:        withoutStoreField("s3Stores", "s3BucketEndpoint"),
			errorContains: "spec.backup.s3Stores[0].s3BucketEndpoint: Required value",
		},
		{
			name:          "s3Stores entry without s3BucketName is rejected",
			mutate:        withoutStoreField("s3Stores", "s3BucketName"),
			errorContains: "spec.backup.s3Stores[0].s3BucketName: Required value",
		},
		{
			name:          "externalConnectivity without type is rejected",
			mutate:        withoutField("spec", "externalConnectivity", "type"),
			errorContains: "spec.externalConnectivity.type: Required value",
		},
		{
			name:          "externalConnectivity with an unknown type is rejected",
			mutate:        withField("nginx", "spec", "externalConnectivity", "type"),
			errorContains: `spec.externalConnectivity.type: Unsupported value: "nginx"`,
		},
		{
			name:          "applicationDatabase version not matching the pattern is rejected",
			mutate:        withField("4.4.10.10", "spec", "applicationDatabase", "version"),
			errorContains: "spec.applicationDatabase.version in body should match",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			om := newOpsManagerUnstructured()
			if tc.mutate != nil {
				tc.mutate(om)
			}
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, om) })

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
