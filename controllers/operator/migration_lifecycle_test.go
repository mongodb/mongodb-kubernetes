package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

// TestMigrationLifecycleStatusTransitions drives the reconciler through the full VM-to-K8s
// migration lifecycle one member at a time, mirroring the E2E promote-and-prune loop.
//
// The fake OM is pre-populated with an RS containing the 3 external VM processes, matching
// the real OM state where VM agents are already registered before any k8s members are added.
func TestMigrationLifecycleStatusTransitions(t *testing.T) {
	ctx := context.Background()

	const totalVMs = 3
	extMembers := []mdbv1.ExternalMember{
		{ProcessName: "vm-0", Hostname: "vm-0.vm-svc.ns.svc.cluster.local", Type: "mongod", ReplicaSetName: "vm-rs"},
		{ProcessName: "vm-1", Hostname: "vm-1.vm-svc.ns.svc.cluster.local", Type: "mongod", ReplicaSetName: "vm-rs"},
		{ProcessName: "vm-2", Hostname: "vm-2.vm-svc.ns.svc.cluster.local", Type: "mongod", ReplicaSetName: "vm-rs"},
	}

	// Start with 1 k8s member (the initial extend step).
	rs := DefaultReplicaSetBuilder().SetMembers(1).Build()
	rs.Spec.ReplicaSetNameOverride = "vm-rs"
	rs.Spec.ExternalMembers = extMembers

	reconciler, kubeClient, _ := defaultReplicaSetReconcilerWithPreloadedMembersFromVMs(ctx, rs)

	// One loop per VM: extend (add k8s member) → prune (remove external) → promote (stable).
	for i := 1; i <= totalVMs; i++ {
		isLast := i == totalVMs

		// Extend: add one k8s member (spec.members: 1→2→3).
		rs.Spec.Members = i
		extCount := len(rs.Spec.ExternalMembers)
		reconcileAndCheckMigratingCondition(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigratingReasonExtending, extCount)

		// Prune: remove the corresponding external VM member.
		rs.Spec.ExternalMembers = rs.Spec.ExternalMembers[:len(rs.Spec.ExternalMembers)-1]
		if !isLast {
			newExtCount := len(rs.Spec.ExternalMembers)
			reconcileAndCheckMigratingCondition(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigratingReasonPruning, newExtCount)

			// Promote: restore votes/priority for the new k8s member (memberConfig change only).
			// In the unit test this is represented by the stable InProgress reconcile.
			reconcileAndCheckMigratingCondition(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigratingReasonInProgress, newExtCount)
		}
	}

	// Last prune: all external members gone → migration complete.
	reconcileAndCheckMigrationAbsent(ctx, t, reconciler, rs, kubeClient)
}

// defaultReplicaSetReconcilerWithPreloadedMembersFromVMs creates a reconciler whose fake OM already
// contains an RS with the external members from rs.Spec.ExternalMembers. This mirrors the
// real OM state where VM agents are pre-registered before the operator adds k8s members.
func defaultReplicaSetReconcilerWithPreloadedMembersFromVMs(ctx context.Context, rs *mdbv1.MongoDB) (*ReconcileMongoDbReplicaSet, kubernetesClient.Client, *om.CachedOMConnectionFactory) {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(rs)
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		mc := connection.(*om.MockedOmConnection)
		// Pre-populate the OM RS with external members, mirroring real OM where VM agents are
		// already RS members before any k8s members are added.
		rsName := rs.Spec.ReplicaSetNameOverride
		processes := make([]om.Process, len(rs.Spec.ExternalMembers))
		for i, m := range rs.Spec.ExternalMembers {
			processes[i] = om.Process{"name": m.ProcessName, "hostname": m.Hostname, "processType": "mongod"}
		}
		omRS := om.NewReplicaSet(rsName, rsName, rs.Spec.Version)
		rsWithProcesses := om.NewReplicaSetWithProcesses(omRS, processes, nil)
		_ = mc.ReadUpdateDeployment(func(d om.Deployment) error {
			d["replicaSets"] = append(d.GetReplicaSets(), rsWithProcesses.Rs)
			return nil
		}, zap.S())
	})
	return newReplicaSetReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", omConnectionFactory.GetConnectionFunc), kubeClient, omConnectionFactory
}

// reconcileAndCheckMigratingCondition pushes the current rs spec to the fake client, runs one
// reconcile, reads back the updated object, and asserts status.conditions Migrating=True reason
// and status.migrationObservedExternalMembersCount for the given external-member count.
func reconcileAndCheckMigratingCondition(
	ctx context.Context,
	t *testing.T,
	reconciler reconcile.Reconciler,
	rs *mdbv1.MongoDB,
	kubeClient kubernetesClient.Client,
	expectedReason mdbstatus.MigratingConditionReason,
	expectedExternalCount int,
) {
	t.Helper()
	require.NoError(t, kubeClient.Update(ctx, rs))
	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, rs.ObjectKey(), rs))
	mig := meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.ConditionMigrating)
	require.NotNil(t, mig, "expected Migrating condition for reason %s", expectedReason)
	require.Equal(t, metav1.ConditionTrue, mig.Status)
	assert.Equal(t, string(expectedReason), mig.Reason,
		"Migrating condition Reason mismatch (migrationObservedExternalMembersCount=%v, status.members=%d)",
		rs.Status.MigrationObservedExternalMembersCount,
		rs.Status.Members)

	require.NotNil(t, rs.Status.MigrationObservedExternalMembersCount)
	assert.Equal(t, expectedExternalCount, *rs.Status.MigrationObservedExternalMembersCount,
		"migrationObservedExternalMembersCount should match len(spec.externalMembers) after reconcile")
	assert.Equal(t, expectedExternalCount, len(rs.Spec.GetExternalMembers()),
		"caller expectedExternalCount should match spec.externalMembers length")
	assert.Nil(t, meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.MigrationObservedExternalMembersConditionType))
}

// reconcileAndCheckMigrationAbsent runs one reconcile and asserts migration-specific status is cleared.
func reconcileAndCheckMigrationAbsent(
	ctx context.Context,
	t *testing.T,
	reconciler reconcile.Reconciler,
	rs *mdbv1.MongoDB,
	kubeClient kubernetesClient.Client,
) {
	t.Helper()
	require.NoError(t, kubeClient.Update(ctx, rs))
	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, rs.ObjectKey(), rs))
	assert.Nil(t, rs.Status.MigrationObservedExternalMembersCount)
	assert.Nil(t, meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.MigrationObservedExternalMembersConditionType))
	assert.Nil(t, meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.ConditionNetworkConnectivityVerified))
	mig := meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.ConditionMigrating)
	require.NotNil(t, mig, "expected Migrating=False after migration completes")
	assert.Equal(t, metav1.ConditionFalse, mig.Status, "expected Migrating=False when no externalMembers remain")
	assert.Equal(t, string(mdbstatus.MigratingReasonComplete), mig.Reason)
	assert.Equal(t, "VM-to-Kubernetes migration finished: all external members removed", mig.Message)
}
