package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

// TestMigrationLifecycleStatusTransitions is a component-level test that drives the
// reconciler through a full VM-to-K8s migration lifecycle and asserts that the
// Migrating=True condition reason transitions correctly at each step,
// and that status.migrationObservedExternalMembersCount matches len(spec.externalMembers) while migrating.
//
// It exercises the full reconciler stack (spec → UpdateStatus → fake-client persistence → next
// reconcile reads prevCount/currentPhase) without requiring a real cluster or Ops Manager.
// The dry-run (Validating) path is covered by TestComputeMigratingConditionReason in
// api/v1/status/migration_lifecycle_test.go and migration/jobrunner_test.go; this test focuses on
// post-annotation Migrating condition reasons.
func TestMigrationLifecycleStatusTransitions(t *testing.T) {
	ctx := context.Background()

	t.Run("target size three then prune down", func(t *testing.T) {
		// 3-member k8s replica set alongside 3 VM external members (no dry-run condition).
		// Prune twice (3→2→1) so migrationObservedExternalMembersCount tracks each count, then clear.
		// DefaultReplicaSetBuilder sets spec.members=3; status.members is 0 until first reconcile,
		// so first reconcile: spec.members(3) > lastReconciled(0) → Migrating reason Extending.
		rs := DefaultReplicaSetBuilder().Build()
		rs.Spec.ReplicaSetNameOverride = "vm-rs"
		rs.Spec.ExternalMembers = []mdbv1.ExternalMember{
			{ProcessName: "vm-0", Hostname: "vm-0.vm-svc.ns.svc.cluster.local", Type: "mongod", ReplicaSetName: "vm-rs"},
			{ProcessName: "vm-1", Hostname: "vm-1.vm-svc.ns.svc.cluster.local", Type: "mongod", ReplicaSetName: "vm-rs"},
			{ProcessName: "vm-2", Hostname: "vm-2.vm-svc.ns.svc.cluster.local", Type: "mongod", ReplicaSetName: "vm-rs"},
		}

		reconciler, kubeClient, _ := defaultReplicaSetReconciler(ctx, nil, "", "", rs)

		// Step 1: k8s=3, prevK8s=0 (initial) → Extending.
		// TODO CLOUDP-362800: This will need 3 reconciles to get to stable. We need to scale up one-by-one even if it's a new deployment.
		// This is something what Lucian fixed
		reconcileAndCheckMigratingCondition(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigratingReasonExtending, 3)

		// Step 2: counts stable → InProgress.
		reconcileAndCheckMigratingCondition(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigratingReasonInProgress, 3)

		// Step 3: Remove first VM member (external count 3→2, prevExt=3) → Pruning.
		rs.Spec.ExternalMembers = rs.Spec.ExternalMembers[1:]
		reconcileAndCheckMigratingCondition(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigratingReasonPruning, 2)

		// Step 4: Prune stabilizes (prevExt now 2 = ext 2) → InProgress.
		reconcileAndCheckMigratingCondition(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigratingReasonInProgress, 2)

		// Step 5: Remove second VM member (external count 2→1) → Pruning, then InProgress.
		rs.Spec.ExternalMembers = rs.Spec.ExternalMembers[1:]
		reconcileAndCheckMigratingCondition(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigratingReasonPruning, 1)
		reconcileAndCheckMigratingCondition(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigratingReasonInProgress, 1)

		// Step 6: Remove last VM member (external count 1→0) → migration cleared.
		rs.Spec.ExternalMembers = nil
		reconcileAndCheckMigrationAbsent(ctx, t, reconciler, rs, kubeClient)
	})
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
	assert.Nil(t, meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.LegacyMigrationObservedExternalMembersConditionType))
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
	assert.Nil(t, meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.LegacyMigrationObservedExternalMembersConditionType))
	assert.Nil(t, meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.ConditionNetworkConnectivityVerified))
	assert.Nil(t, meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.LegacyNetworkConnectivityVerificationConditionType))
	mig := meta.FindStatusCondition(rs.Status.Conditions, mdbstatus.ConditionMigrating)
	require.NotNil(t, mig, "expected Migrating=False after migration completes")
	assert.Equal(t, metav1.ConditionFalse, mig.Status, "expected Migrating=False when no externalMembers remain")
	assert.Equal(t, string(mdbstatus.MigratingReasonComplete), mig.Reason)
	assert.Equal(t, "VM-to-Kubernetes migration finished: all external members removed", mig.Message)
}
