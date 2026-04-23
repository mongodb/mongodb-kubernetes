package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/test"
)

// TestNamedShards_EquivalenceToShardCount reconciles two sharded clusters
// that are logically identical but expressed via the two alternative forms
// (spec.shardCount vs spec.shards with identity-preserving names). It proves
// that both forms produce the same set of StatefulSets, Services, Secrets,
// and Ops Manager deployment JSON.
func TestNamedShards_EquivalenceToShardCount(t *testing.T) {
	ctx := context.Background()

	scCount := test.DefaultClusterBuilder().
		SetName("mdbs").
		SetShardCountSpec(2).
		Build()

	scNamed := test.DefaultClusterBuilder().
		SetName("mdbs").
		SetShardsSpec([]mdbv1.Shard{
			{ShardName: "mdbs-0"},
			{ShardName: "mdbs-1"},
		}).
		Build()

	// Reconcile both clusters on independent fake clients.
	rCount, _, clCount, omfCount, err := defaultShardedClusterReconciler(ctx, nil, "", "", scCount, nil, testBackupEnableDelay)
	require.NoError(t, err)
	checkReconcileSuccessful(ctx, t, rCount, scCount, clCount)

	rNamed, _, clNamed, omfNamed, err := defaultShardedClusterReconciler(ctx, nil, "", "", scNamed, nil, testBackupEnableDelay)
	require.NoError(t, err)
	checkReconcileSuccessful(ctx, t, rNamed, scNamed, clNamed)

	// Kubernetes resources must have the same set of names.
	assertSameObjectKeys(t, clCount, clNamed, &appsv1.StatefulSet{}, "StatefulSets")
	assertSameObjectKeys(t, clCount, clNamed, &corev1.Service{}, "Services")
	assertSameObjectKeys(t, clCount, clNamed, &corev1.Secret{}, "Secrets")

	// Mocked OM deployment JSON must have identical shard configuration
	// (shards array, config server replica set, processes).
	depCount := omfCount.GetConnection().(*om.MockedOmConnection).GetDeployment()
	depNamed := omfNamed.GetConnection().(*om.MockedOmConnection).GetDeployment()
	assert.Equal(t, depCount.ShardedClustersCopy(), depNamed.ShardedClustersCopy(),
		"sharding configuration in Ops Manager deployment must be identical")
}

// TestNamedShards_MigrationInPlaceNoChurn is the core safety proof. It
// reconciles a cluster declared with spec.shardCount until steady state,
// snapshots every StatefulSet's Spec and the mocked OM deployment, flips
// the CR to the identity-preserving spec.shards form, reconciles again, and
// asserts that both the StatefulSet Specs and the Ops Manager sharded-cluster
// configuration are byte-identical.
//
// We compare Specs rather than ResourceVersions because the fake client bumps
// ResourceVersion on every Update call. The meaningful invariant is "the
// shape of the resources stored in k8s / OM did not change", which is what
// tells us the pods will not be restarted.
func TestNamedShards_MigrationInPlaceNoChurn(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		SetName("mdbs").
		SetShardCountSpec(2).
		Build()

	reconciler, _, cl, omf, err := defaultShardedClusterReconciler(ctx, nil, "", "", sc, nil, testBackupEnableDelay)
	require.NoError(t, err)
	// Run two reconciles to settle any first-reconcile-only churn.
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)

	stsSpecsBefore := snapshotStsSpecs(t, cl)
	omBefore := omf.GetConnection().(*om.MockedOmConnection).GetDeployment().ShardedClustersCopy()

	// Fetch latest CR and flip to named shards with identity-preserving names.
	require.NoError(t, cl.Get(ctx, kube.ObjectKeyFromApiObject(sc), sc))
	sc.Spec.ShardCount = 0
	sc.Spec.Shards = []mdbv1.Shard{
		{ShardName: "mdbs-0"},
		{ShardName: "mdbs-1"},
	}
	require.NoError(t, cl.Update(ctx, sc))
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)

	stsSpecsAfter := snapshotStsSpecs(t, cl)
	assert.Equal(t, stsSpecsBefore, stsSpecsAfter,
		"shard StatefulSet specs must be unchanged after migration to named shards")

	omAfter := omf.GetConnection().(*om.MockedOmConnection).GetDeployment().ShardedClustersCopy()
	assert.Equal(t, omBefore, omAfter,
		"Ops Manager sharded-cluster configuration must be unchanged after migration to named shards")
}

// TestNamedShards_MigrationPreservesShardStsNames asserts that after flipping
// from spec.shardCount=N to spec.shards with identity-preserving names, the
// set of shard StatefulSet names is exactly {mdbs-0, mdbs-1} — the same as
// before.
func TestNamedShards_MigrationPreservesShardStsNames(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		SetName("mdbs").
		SetShardCountSpec(2).
		Build()

	reconciler, _, cl, _, err := defaultShardedClusterReconciler(ctx, nil, "", "", sc, nil, testBackupEnableDelay)
	require.NoError(t, err)
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)

	require.NoError(t, cl.Get(ctx, kube.ObjectKeyFromApiObject(sc), sc))
	sc.Spec.ShardCount = 0
	sc.Spec.Shards = []mdbv1.Shard{
		{ShardName: "mdbs-0"},
		{ShardName: "mdbs-1"},
	}
	require.NoError(t, cl.Update(ctx, sc))
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)

	for _, name := range []string{"mdbs-0", "mdbs-1"} {
		sts := &appsv1.StatefulSet{}
		err := cl.Get(ctx, kube.ObjectKey(sc.Namespace, name), sts)
		require.NoError(t, err, "shard StatefulSet %q must still exist after migration", name)
	}
}

// TestNamedShards_ReconcileWithShardsFromScratch reconciles a new cluster
// that uses spec.shards directly (no prior shardCount state). Sanity check
// that the named form is a fully supported create path.
func TestNamedShards_ReconcileWithShardsFromScratch(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		SetName("mdbs").
		SetShardsSpec([]mdbv1.Shard{
			{ShardName: "mdbs-0"},
			{ShardName: "mdbs-1"},
		}).
		Build()

	reconciler, _, cl, _, err := defaultShardedClusterReconciler(ctx, nil, "", "", sc, nil, testBackupEnableDelay)
	require.NoError(t, err)
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)

	assert.Len(t, mock.GetMapForObject(cl, &appsv1.StatefulSet{}), 4,
		"expected 2 shard STS + config server + mongos")

	for _, name := range []string{"mdbs-0", "mdbs-1"} {
		sts := &appsv1.StatefulSet{}
		require.NoError(t, cl.Get(ctx, kube.ObjectKey(sc.Namespace, name), sts))
	}
}

// TestNamedShards_RemoveMiddleShardDeletesCorrectSts reconciles a cluster with
// three named shards, then flips the spec to remove the middle shard by name.
// Proves that removeUnusedStatefulsets deletes the StatefulSet whose name was
// dropped (not the tail positional one, which was the pre-fix behaviour).
func TestNamedShards_RemoveMiddleShardDeletesCorrectSts(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		SetName("mdbs").
		SetShardsSpec([]mdbv1.Shard{
			{ShardName: "mdbs-0"},
			{ShardName: "mdbs-1"},
			{ShardName: "mdbs-2"},
		}).
		Build()

	reconciler, _, cl, _, err := defaultShardedClusterReconciler(ctx, nil, "", "", sc, nil, testBackupEnableDelay)
	require.NoError(t, err)
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)

	for _, name := range []string{"mdbs-0", "mdbs-1", "mdbs-2"} {
		sts := &appsv1.StatefulSet{}
		require.NoError(t, cl.Get(ctx, kube.ObjectKey(sc.Namespace, name), sts),
			"shard STS %q must exist after initial reconcile", name)
	}

	// Drop the middle shard by name.
	require.NoError(t, cl.Get(ctx, kube.ObjectKeyFromApiObject(sc), sc))
	sc.Spec.Shards = []mdbv1.Shard{
		{ShardName: "mdbs-0"},
		{ShardName: "mdbs-2"},
	}
	require.NoError(t, cl.Update(ctx, sc))
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)

	// mdbs-1 must be gone; mdbs-0 and mdbs-2 must remain.
	err = cl.Get(ctx, kube.ObjectKey(sc.Namespace, "mdbs-1"), &appsv1.StatefulSet{})
	assert.True(t, err != nil, "shard STS mdbs-1 must be deleted after removal")

	for _, name := range []string{"mdbs-0", "mdbs-2"} {
		sts := &appsv1.StatefulSet{}
		require.NoError(t, cl.Get(ctx, kube.ObjectKey(sc.Namespace, name), sts),
			"shard STS %q must still exist", name)
	}
}

// TestNamedShards_RemoveMiddleShardDoesNotCreateSpuriousSts reproduces the
// exact scenario from the e2e test TestRemoveIndexBasedShard:
//  1. Start with [mdbs-0, mdbs-1, mdbs-2, extra-shard-alpha] (4 shards, the
//     last one has a name unrelated to the synthesised <mdb>-<idx> scheme).
//  2. Drop the middle mdbs-1.
//
// After step 2, status.ShardCount=4 while spec.ShardCount=3. The reconciler
// iterates up to max(spec, status)=4. At position 3, the old
// ShardRsName(3) falls back to the synthesised "mdbs-3" — which is NOT the
// name of any deployed shard. That produces a spurious "mdbs-3" StatefulSet
// instead of processing the shard that was actually removed (mdbs-1).
func TestNamedShards_RemoveMiddleShardDoesNotCreateSpuriousSts(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		SetName("mdbs").
		SetShardsSpec([]mdbv1.Shard{
			{ShardName: "mdbs-0"},
			{ShardName: "mdbs-1"},
			{ShardName: "mdbs-2"},
			{ShardName: "extra-shard-alpha"},
		}).
		Build()

	reconciler, _, cl, omf, err := defaultShardedClusterReconciler(ctx, nil, "", "", sc, nil, testBackupEnableDelay)
	require.NoError(t, err)
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)

	for _, name := range []string{"mdbs-0", "mdbs-1", "mdbs-2", "extra-shard-alpha"} {
		err := cl.Get(ctx, kube.ObjectKey(sc.Namespace, name), &appsv1.StatefulSet{})
		require.NoError(t, err, "initial reconcile must create STS %q", name)
	}

	// Now drop the middle shard (mdbs-1).
	require.NoError(t, cl.Get(ctx, kube.ObjectKeyFromApiObject(sc), sc))
	sc.Spec.Shards = []mdbv1.Shard{
		{ShardName: "mdbs-0"},
		{ShardName: "mdbs-2"},
		{ShardName: "extra-shard-alpha"},
	}
	require.NoError(t, cl.Update(ctx, sc))
	checkReconcileSuccessful(ctx, t, reconciler, sc, cl)

	// The spurious "mdbs-3" STS must never be created.
	err = cl.Get(ctx, kube.ObjectKey(sc.Namespace, "mdbs-3"), &appsv1.StatefulSet{})
	assert.True(t, err != nil,
		"synthesised tail name 'mdbs-3' must not be created when spec.shards has 3 items (was the bug)")

	// mdbs-1 must be deleted.
	err = cl.Get(ctx, kube.ObjectKey(sc.Namespace, "mdbs-1"), &appsv1.StatefulSet{})
	assert.True(t, err != nil, "shard STS mdbs-1 must be deleted after removal")

	// Remaining shards must still be there.
	for _, name := range []string{"mdbs-0", "mdbs-2", "extra-shard-alpha"} {
		err := cl.Get(ctx, kube.ObjectKey(sc.Namespace, name), &appsv1.StatefulSet{})
		require.NoError(t, err, "shard STS %q must still exist", name)
	}

	// Exactly 3 shard STSes + config + mongos = 5.
	stsList := mock.GetMapForObject(cl, &appsv1.StatefulSet{})
	assert.Len(t, stsList, 5, "expected 3 shards + config + mongos, got: %v", stsKeys(stsList))

	// OM-side assertions: after a fully successful reconcile (mocked OM completes
	// both publishDeployment passes synchronously — first publish marks mdbs-1 for
	// draining, WaitForReadyState returns immediately in the mock, second publish
	// with finalizing=true removes mdbs-1 entirely), the final state must be:
	//   - shards[] contains only the desired {mdbs-0, mdbs-2, extra-shard-alpha}
	//   - draining[] is empty (drain "completed" in the mock)
	//   - no mdbs-1 processes remain
	// This proves the OM publish path now constructs the correct desired config
	// for a middle-shard removal (pre-fix, publish iterated up to
	// max(spec.ShardCount, status.ShardCount) using ShardRsName(i), which at
	// position 3 returned the synthesised "mdbs-3" and included a bogus shard in
	// the published config).
	dep := omf.GetConnection().(*om.MockedOmConnection).GetDeployment()
	scs := dep.ShardedClustersCopy()
	require.Len(t, scs, 1, "expected one sharded cluster")
	sharded := scs[0]

	shardIDsInOM := make([]string, 0)
	for _, sh := range sharded["shards"].([]om.Shard) {
		shardIDsInOM = append(shardIDsInOM, sh["_id"].(string))
	}
	assert.ElementsMatch(t, []string{"mdbs-0", "mdbs-2", "extra-shard-alpha"}, shardIDsInOM,
		"shards[] in OM sharded-cluster must be exactly the desired set and must NOT contain a bogus 'mdbs-3'")
	assert.NotContains(t, shardIDsInOM, "mdbs-3",
		"synthesised tail name 'mdbs-3' must not appear in OM shards[] (was the core bug)")

	// After finalizing, mdbs-1 processes are gone.
	procNames := dep.GetAllProcessNames()
	for _, pn := range procNames {
		assert.False(t, pn == "mdbs-1-0" || pn == "mdbs-1-1" || pn == "mdbs-1-2",
			"mdbs-1 process %q must be removed after drain completes", pn)
		assert.False(t, pn == "mdbs-3-0" || pn == "mdbs-3-1" || pn == "mdbs-3-2",
			"bogus mdbs-3 process %q must never appear", pn)
	}
}

// --- helpers ---

// snapshotStsSpecs returns a map[stsName]StatefulSetSpec, stripped of the
// server-populated ResourceVersion fields. Equal snapshots across two points
// in time mean the reconciler did not actually change the desired state
// (pods would not have been restarted in a real cluster).
func snapshotStsSpecs(t *testing.T, cl client.Client) map[string]appsv1.StatefulSetSpec {
	t.Helper()
	objs := mock.GetMapForObject(cl, &appsv1.StatefulSet{})
	out := make(map[string]appsv1.StatefulSetSpec, len(objs))
	for k, obj := range objs {
		sts, ok := obj.(*appsv1.StatefulSet)
		require.True(t, ok)
		out[k.Name] = sts.Spec
	}
	return out
}

func stsKeys(m map[client.ObjectKey]apiruntime.Object) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k.Name)
	}
	return out
}

func assertSameObjectKeys(t *testing.T, a, b client.Client, sample apiruntime.Object, kind string) {
	t.Helper()
	keysA := make(map[client.ObjectKey]struct{})
	for k := range mock.GetMapForObject(a, sample) {
		keysA[client.ObjectKey{Namespace: k.Namespace, Name: k.Name}] = struct{}{}
	}
	keysB := make(map[client.ObjectKey]struct{})
	for k := range mock.GetMapForObject(b, sample) {
		keysB[client.ObjectKey{Namespace: k.Namespace, Name: k.Name}] = struct{}{}
	}
	assert.Equal(t, keysA, keysB, "%s name sets differ between shardCount and named shards clusters", kind)
}
