package mdb_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMongoDBStatus_LicenseConditionRoundTrip(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client
	rs := newMongoDB(t, nil)
	require.NoError(t, k8sClient.Create(ctx, rs))
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, rs)
	})

	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(rs), rs))
	rs.Status.Phase = status.PhaseFailed
	meta.SetStatusCondition(&rs.Status.Conditions, status.LicenseEnforcedCondition("not licensed"))
	meta.SetStatusCondition(&rs.Status.Conditions, status.MigratingCondition(false, status.MigratingReasonInProgress))
	require.NoError(t, k8sClient.Status().Update(ctx, rs))

	got := &mdbv1.MongoDB{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(rs), got))
	ready := meta.FindStatusCondition(got.Status.Conditions, status.ConditionOpsManagerLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, status.ReasonLicenseEnforced, ready.Reason)
	assert.Equal(t, "not licensed", ready.Message)
	require.NotNil(t, meta.FindStatusCondition(got.Status.Conditions, status.ConditionMigrating))
}
