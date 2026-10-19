package om_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestOpsManager_LicenseSpecAndStatusRoundTrip(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client
	om := newOpsManager(t, func(om *omv1.MongoDBOpsManager) {
		om.Spec.AppDB = &omv1.AppDBSpec{
			Version: "6.0.0",
			MonitoringAgent: mdbv1.MonitoringAgentConfig{
				StartupParameters: mdbv1.StartupParameters{},
			},
		}
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			LicenseSecretRef: &omv1.SecretRef{Name: "om-license"},
		}
	})
	require.NoError(t, k8sClient.Create(ctx, om))
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, om)
	})

	got := &omv1.MongoDBOpsManager{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(om), got))
	require.NotNil(t, got.Spec.License)
	require.NotNil(t, got.Spec.License.LicenseSecretRef)
	assert.Equal(t, "om-license", got.Spec.License.LicenseSecretRef.Name)

	got.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{
			EffectiveState: "LICENSED",
			Enforced:       true,
			Cause:          "ok",
		},
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{
			{ArtifactID: "art-1", Usable: true},
		},
	}
	require.NoError(t, k8sClient.Status().Update(ctx, got))

	stored := &omv1.MongoDBOpsManager{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(om), stored))
	require.NotNil(t, stored.Status.OpsManagerStatus.License)
	require.NotNil(t, stored.Status.OpsManagerStatus.License.Enforcement)
	assert.Equal(t, "LICENSED", stored.Status.OpsManagerStatus.License.Enforcement.EffectiveState)
	assert.True(t, stored.Status.OpsManagerStatus.License.Enforcement.Enforced)
	require.Len(t, stored.Status.OpsManagerStatus.License.Licenses, 1)
	assert.Equal(t, "art-1", stored.Status.OpsManagerStatus.License.Licenses[0].ArtifactID)
}
