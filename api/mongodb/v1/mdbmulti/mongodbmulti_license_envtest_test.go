package mdbmulti_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMongoDBMultiClusterStatus_LicenseConditionRoundTrip(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client
	mrs := newMongoDBMultiCluster(t, "")
	require.NoError(t, k8sClient.Create(ctx, mrs))
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, mrs)
	})

	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(mrs), mrs))
	mrs.Status.Phase = status.PhaseRunning
	meta.SetStatusCondition(&mrs.Status.Conditions, status.LicenseEnforcedCondition("not licensed"))
	require.NoError(t, k8sClient.Status().Update(ctx, mrs))

	got := &mcdbmulti.MongoDBMultiCluster{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(mrs), got))
	ready := meta.FindStatusCondition(got.Status.Conditions, status.ConditionOpsManagerLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, status.ReasonLicenseEnforced, ready.Reason)
	assert.Equal(t, "not licensed", ready.Message)
}
