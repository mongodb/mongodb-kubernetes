package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

// TestMigrationLifecycleStatusTransitions is a component-level test that drives the
// reconciler through a full VM-to-K8s migration lifecycle and asserts that
// status.migration.phase transitions correctly at each step.
//
// It exercises the full reconciler stack (spec → UpdateStatus → fake-client persistence → next
// reconcile reads prevCount/currentPhase) without requiring a real cluster or Ops Manager.
// The dry-run (Validating) path is covered in api/v1/status/migration_lifecycle_test.go and
// migration/jobrunner_test.go; this test focuses on the post-annotation phase transitions.
//
// Lifecycle exercised here (no dry-run):
//
//	Extending (spec.members=3 > lastReconciled=0) → InProgress (spec==lastReconciled) →
//	Pruning (ext count drops) → InProgress (ext stable) →
//	nil (all VMs removed, status.migration cleared)
func TestMigrationLifecycleStatusTransitions(t *testing.T) {
	ctx := context.Background()

	// 3-member k8s replica set alongside 3 VM external members (no dry-run condition).
	// DefaultReplicaSetBuilder sets spec.members=3; status.members is 0 until first reconcile,
	// so first reconcile: spec.members(3) > lastReconciled(0) → Extending.
	rs := DefaultReplicaSetBuilder().Build()
	rs.Spec.ReplicaSetNameOverride = "vm-rs"
	rs.Spec.ExternalMembers = []mdbv1.ExternalMember{
		{ProcessName: "vm-0", Hostname: "vm-0.vm-svc.ns.svc.cluster.local", Type: "mongod", ReplicaSetName: "vm-rs"},
		{ProcessName: "vm-1", Hostname: "vm-1.vm-svc.ns.svc.cluster.local", Type: "mongod", ReplicaSetName: "vm-rs"},
		{ProcessName: "vm-2", Hostname: "vm-2.vm-svc.ns.svc.cluster.local", Type: "mongod", ReplicaSetName: "vm-rs"},
	}

	reconciler, kubeClient, _ := defaultReplicaSetReconciler(ctx, nil, "", "", rs)

	// Step 1: k8s=3, prevK8s=0 (initial) → Extending.
	reconcileAndCheckMigrationPhase(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigrationPhaseExtending)

	// Step 2: counts stable → InProgress.
	reconcileAndCheckMigrationPhase(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigrationPhaseInProgress)

	// Step 3: Remove first VM member (external count 3→2, prevExt=3) → Pruning.
	rs.Spec.ExternalMembers = rs.Spec.ExternalMembers[1:]
	reconcileAndCheckMigrationPhase(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigrationPhasePruning)

	// Step 4: Prune stabilizes (prevExt now 2 = ext 2) → InProgress.
	reconcileAndCheckMigrationPhase(ctx, t, reconciler, rs, kubeClient, mdbstatus.MigrationPhaseInProgress)

	// Step 5: Remove remaining VM members (external count 2→0) → migration cleared.
	rs.Spec.ExternalMembers = nil
	reconcileAndCheckMigrationAbsent(ctx, t, reconciler, rs, kubeClient)
}

// reconcileAndCheckMigrationPhase pushes the current rs spec to the fake client, runs one
// reconcile, reads back the updated object, and asserts status.migration.phase.
func reconcileAndCheckMigrationPhase(
	ctx context.Context,
	t *testing.T,
	reconciler reconcile.Reconciler,
	rs *mdbv1.MongoDB,
	kubeClient kubernetesClient.Client,
	expectedPhase mdbstatus.MigrationLifecyclePhase,
) {
	t.Helper()
	require.NoError(t, kubeClient.Update(ctx, rs))
	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, rs.ObjectKey(), rs))
	require.NotNil(t, rs.Status.Migration, "expected status.migration to be set for phase %s", expectedPhase)
	assert.Equal(t, expectedPhase, rs.Status.Migration.Phase,
		"status.migration.phase mismatch (observedExternalMembersCount=%d, status.members=%d)",
		rs.Status.Migration.ObservedExternalMembersCount,
		rs.Status.Members)
}

// reconcileAndCheckMigrationAbsent runs one reconcile and asserts that
// status.migration has been cleared (nil) after all externalMembers are removed.
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
	assert.Nil(t, rs.Status.Migration, "expected status.migration to be nil when no externalMembers remain")
}
