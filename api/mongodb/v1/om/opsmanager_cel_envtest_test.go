package om_test

import (
	"context"
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
