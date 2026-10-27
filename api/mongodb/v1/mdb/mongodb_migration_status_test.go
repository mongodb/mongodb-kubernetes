package mdb

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// This file covers the top-level Migrating condition for both migration topologies: the ReplicaSet
// tests come first, then the ShardedCluster ones. The two share a contract worth reading side by
// side — an empty spec.externalMembers only completes the migration once the reconcile reaches
// PhaseRunning — so each topology has the same DoesNotCompleteBeforeRunning / DoesNotCompleteOnFailed
// / CompletesOnRunning trio.

// rsMigrationTestResource builds a 3-member ReplicaSet MongoDB with the given external (VM) members
// and dry-run annotation. Callers set m.Status.Members for the prior reconciled count, or pass it to
// applyComputedReplicaSetMigrationStatus directly.
func rsMigrationTestResource(ext []ExternalMember, dryRun bool) *MongoDB {
	ann := map[string]string{}
	if dryRun {
		ann[util.MigrationDryRunAnnotation] = strconv.FormatBool(dryRun)
	}
	m := &MongoDB{
		ObjectMeta: metav1.ObjectMeta{Annotations: ann},
		Spec: MongoDbSpec{
			DbCommonSpec:    DbCommonSpec{ResourceType: ReplicaSet},
			ExternalMembers: ext,
		},
	}
	m.Spec.Members = 3
	return m
}

func TestApplyComputedReplicaSetMigrationStatus_DryRunValidating(t *testing.T) {
	m := rsMigrationTestResource(oneExternalMember(), true)
	m.Status.Members = 3
	m.applyComputedReplicaSetMigrationStatus(status.PhaseRunning, 3)
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionTrue, mig.Status)
	assert.Equal(t, string(status.MigratingReasonValidating), mig.Reason)
}

func TestApplyComputedReplicaSetMigrationStatus_Extending(t *testing.T) {
	m := rsMigrationTestResource(oneExternalMember(), false)
	m.applyComputedReplicaSetMigrationStatus(status.PhaseRunning, 0) // spec.members 3 > prior 0 => Extending
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, string(status.MigratingReasonExtending), mig.Reason)
}

func TestApplyComputedReplicaSetMigrationStatus_InProgressStable(t *testing.T) {
	m := rsMigrationTestResource(oneExternalMember(), false)
	prev := 1
	m.Status.MigrationObservedExternalMembersCount = &prev
	m.applyComputedReplicaSetMigrationStatus(status.PhaseRunning, 3) // prior == spec.members, external unchanged => InProgress
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, string(status.MigratingReasonInProgress), mig.Reason)
}

func TestApplyComputedReplicaSetMigrationStatus_Pruning(t *testing.T) {
	m := rsMigrationTestResource(oneExternalMember(), false)
	prev := 2 // was 2 external, now 1 => Pruning (spec.members 3 == prior 3, so not Extending)
	m.Status.MigrationObservedExternalMembersCount = &prev
	m.applyComputedReplicaSetMigrationStatus(status.PhaseRunning, 3)
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, string(status.MigratingReasonPruning), mig.Reason)
}

func TestApplyComputedReplicaSetMigrationStatus_CompleteClears(t *testing.T) {
	m := rsMigrationTestResource(nil, false) // no external members remain
	meta.SetStatusCondition(&m.Status.Conditions, status.MigratingCondition(true, status.MigratingReasonInProgress))
	meta.SetStatusCondition(&m.Status.Conditions, status.MigrationCondition(status.MigrationPhaseConnectivityCheckPassed, "NetworkValidationPassed", "ok"))
	prev := 1
	m.Status.MigrationObservedExternalMembersCount = &prev

	m.applyComputedReplicaSetMigrationStatus(status.PhaseRunning, 3)

	assert.Nil(t, m.Status.MigrationObservedExternalMembersCount)
	assert.Nil(t, meta.FindStatusCondition(m.Status.Conditions, status.ConditionNetworkConnectivityVerified))
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionFalse, mig.Status)
	assert.Equal(t, string(status.MigratingReasonComplete), mig.Reason)
}

func TestUpdateStatus_ReplicaSet_SetsMigratingCondition(t *testing.T) {
	// Proves the ReplicaSet branch of UpdateStatus wires in the computation and applies the members
	// option before computing (dry-run => Validating regardless of counts).
	m := rsMigrationTestResource(oneExternalMember(), true)
	m.UpdateStatus(status.PhasePending, status.ReplicaSetMembersOption{Members: 3})
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig, "ReplicaSet UpdateStatus must compute the Migrating condition")
	assert.Equal(t, string(status.MigratingReasonValidating), mig.Reason)
	assert.Equal(t, 3, m.Status.Members)
}

func TestUpdateStatus_ReplicaSet_ScalingUpIsExtending(t *testing.T) {
	// Regression: the prior count must be read from m.Status.Members *before* the members option
	// overwrites it. If the option value (already the new target) were used as the prior, spec.members
	// would never exceed it and Extending would be missed while the new pods are still coming up.
	m := rsMigrationTestResource(oneExternalMember(), false)
	m.Spec.Members = 5 // spec now wants 5 members
	m.Status.Members = 3
	prev := 1
	m.Status.MigrationObservedExternalMembersCount = &prev

	m.UpdateStatus(status.PhasePending, status.ReplicaSetMembersOption{Members: 5})

	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, string(status.MigratingReasonExtending), mig.Reason)
}

// rsMigrationJustFinishedInSpec returns a ReplicaSet whose last external member has just been
// removed from the spec, with the status still carrying an in-flight migration from the previous
// reconcile.
func rsMigrationJustFinishedInSpec() *MongoDB {
	m := rsMigrationTestResource(nil, false)
	meta.SetStatusCondition(&m.Status.Conditions, status.MigratingCondition(true, status.MigratingReasonPruning))
	meta.SetStatusCondition(&m.Status.Conditions, status.MigrationCondition(
		status.MigrationPhaseConnectivityCheckPassed, "NetworkValidationPassed", "ok"))
	prev := 1
	m.Status.MigrationObservedExternalMembersCount = &prev
	return m
}

func TestUpdateStatus_ReplicaSet_DoesNotCompleteBeforeRunning(t *testing.T) {
	// Mirrors the ShardedCluster case: the last VM is gone from the spec, but until this reconcile
	// succeeds the operator has not finished removing those processes from the automation config.
	m := rsMigrationJustFinishedInSpec()

	m.UpdateStatus(status.PhasePending)

	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionTrue, mig.Status, "migration must still read as active until the reconcile succeeds")
	assert.Equal(t, string(status.MigratingReasonPruning), mig.Reason,
		"the prior reason must be left as-is, not recomputed from a now-empty spec")
	assert.NotNil(t, meta.FindStatusCondition(m.Status.Conditions, status.ConditionNetworkConnectivityVerified))
	require.NotNil(t, m.Status.MigrationObservedExternalMembersCount)
	assert.Equal(t, 1, *m.Status.MigrationObservedExternalMembersCount)
}

func TestUpdateStatus_ReplicaSet_DoesNotCompleteOnFailed(t *testing.T) {
	m := rsMigrationJustFinishedInSpec()

	m.UpdateStatus(status.PhaseFailed)

	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionTrue, mig.Status, "a failed reconcile must not report the migration as complete")
	assert.Equal(t, string(status.MigratingReasonPruning), mig.Reason)
}

func TestUpdateStatus_ReplicaSet_CompletesOnRunning(t *testing.T) {
	m := rsMigrationJustFinishedInSpec()

	m.UpdateStatus(status.PhaseRunning)

	assert.Nil(t, m.Status.MigrationObservedExternalMembersCount)
	assert.Nil(t, meta.FindStatusCondition(m.Status.Conditions, status.ConditionNetworkConnectivityVerified))
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionFalse, mig.Status)
	assert.Equal(t, string(status.MigratingReasonComplete), mig.Reason)
}

// uniformSize builds an override-free single-cluster size breakdown: perShard mongods on every
// shard, plus config-server mongods and mongos, all on the default member cluster.
func uniformSize(perShardMongods, configMongods, mongos int) *status.MongodbShardedSizeStatusInClusters {
	return &status.MongodbShardedSizeStatusInClusters{
		ShardMongodsInClusters:        map[string]int{"__default": perShardMongods},
		ShardOverridesInClusters:      map[string]map[string]int{},
		ConfigServerMongodsInClusters: map[string]int{"__default": configMongods},
		MongosCountInClusters:         map[string]int{"__default": mongos},
	}
}

// shardedMigrationTestResource builds a 2-shard ShardedCluster MongoDB with the given external (VM)
// members and dry-run annotation. Its spec desired member count is 2*3 + 3 config + 2 mongos = 11,
// mirroring uniformSize(3, 3, 2). Callers set m.Status.SizeStatusInClusters for the prior reconciled
// size and pass the prior override-aware count to applyComputedShardedClusterMigrationStatus.
func shardedMigrationTestResource(ext []ExternalMember, dryRun bool) *MongoDB {
	ann := map[string]string{}
	if dryRun {
		ann[util.MigrationDryRunAnnotation] = strconv.FormatBool(dryRun)
	}
	m := &MongoDB{
		ObjectMeta: metav1.ObjectMeta{Annotations: ann},
		Spec: MongoDbSpec{
			DbCommonSpec:    DbCommonSpec{ResourceType: ShardedCluster},
			ExternalMembers: ext,
		},
	}
	m.Spec.ShardCount = 2
	m.Spec.MongodsPerShardCount = 3
	m.Spec.ConfigServerCount = 3
	m.Spec.MongosCount = 2
	return m
}

func oneExternalMember() []ExternalMember {
	return []ExternalMember{{ProcessName: "vm-0", Hostname: "h0:27017", Type: "mongod"}}
}

func TestShardedInClusterMemberCount_Uniform(t *testing.T) {
	// 2 shards * 3 mongods + 3 config + 2 mongos = 11
	assert.Equal(t, 11, shardedClusterInClusterMemberCount(2, uniformSize(3, 3, 2)))
}

func TestShardedInClusterMemberCount_WithOverride(t *testing.T) {
	// shard-0 uses the common 3 mongods; shard-1 is overridden to 5. + 3 config + 2 mongos = 13
	s := &status.MongodbShardedSizeStatusInClusters{
		ShardMongodsInClusters:        map[string]int{"__default": 3},
		ShardOverridesInClusters:      map[string]map[string]int{"sc-1": {"__default": 5}},
		ConfigServerMongodsInClusters: map[string]int{"__default": 3},
		MongosCountInClusters:         map[string]int{"__default": 2},
	}
	assert.Equal(t, 13, shardedClusterInClusterMemberCount(2, s))
}

func TestShardedInClusterMemberCount_Nil(t *testing.T) {
	assert.Equal(t, 0, shardedClusterInClusterMemberCount(2, nil))
}

func TestShardedSpecMemberCount_Uniform(t *testing.T) {
	// 2 shards * 3 mongods + 3 config + 2 mongos = 11
	m := shardedMigrationTestResource(nil, false)
	assert.Equal(t, 11, m.shardedClusterSpecMemberCount())
}

func TestShardedSpecMemberCount_WithOverride(t *testing.T) {
	// shard-0 uses the default 3 mongods; shard-1 is overridden to 5. + 3 config + 2 mongos = 13
	m := shardedMigrationTestResource(nil, false)
	m.Name = "sc"
	five := 5
	m.Spec.ShardOverrides = []ShardOverride{{ShardNames: []string{"sc-1"}, Members: &five}}
	assert.Equal(t, 13, m.shardedClusterSpecMemberCount())
}

func TestApplyComputedShardedMigrationStatus_DryRunValidating(t *testing.T) {
	m := shardedMigrationTestResource(oneExternalMember(), true)
	m.Status.SizeStatusInClusters = uniformSize(3, 3, 2)
	m.applyComputedShardedClusterMigrationStatus(status.PhaseRunning, 0)
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionTrue, mig.Status)
	assert.Equal(t, string(status.MigratingReasonValidating), mig.Reason)
}

func TestApplyComputedShardedMigrationStatus_Extending(t *testing.T) {
	m := shardedMigrationTestResource(oneExternalMember(), false)
	m.applyComputedShardedClusterMigrationStatus(status.PhaseRunning, 0) // spec desired 11 > prior 0 => Extending
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, string(status.MigratingReasonExtending), mig.Reason)
}

func TestApplyComputedShardedMigrationStatus_InProgressStable(t *testing.T) {
	m := shardedMigrationTestResource(oneExternalMember(), false)
	prev := 1
	m.Status.MigrationObservedExternalMembersCount = &prev
	m.applyComputedShardedClusterMigrationStatus(status.PhaseRunning, 11) // prior == spec desired, external unchanged => InProgress
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, string(status.MigratingReasonInProgress), mig.Reason)
}

func TestApplyComputedShardedMigrationStatus_Pruning(t *testing.T) {
	m := shardedMigrationTestResource(oneExternalMember(), false)
	prev := 2 // was 2 external, now 1 => Pruning (spec desired 11 == prior 11, so not Extending)
	m.Status.MigrationObservedExternalMembersCount = &prev
	m.applyComputedShardedClusterMigrationStatus(status.PhaseRunning, 11)
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, string(status.MigratingReasonPruning), mig.Reason)
}

func TestApplyComputedShardedMigrationStatus_StableOverriddenIsInProgress(t *testing.T) {
	// Regression: a stable, fully-provisioned overridden cluster must report InProgress, not a
	// spurious Extending. The desired count (spec, override-aware) equals the prior reconciled count,
	// so the reason must not be Extending even though status/spec differ from the uniform case.
	m := shardedMigrationTestResource(oneExternalMember(), false)
	m.Name = "sc"
	five := 5
	m.Spec.ShardOverrides = []ShardOverride{{ShardNames: []string{"sc-1"}, Members: &five}}
	prev := 1
	m.Status.MigrationObservedExternalMembersCount = &prev
	m.applyComputedShardedClusterMigrationStatus(status.PhaseRunning, 13) // spec desired 13 == prior 13 => InProgress
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, string(status.MigratingReasonInProgress), mig.Reason)
}

func TestApplyComputedShardedMigrationStatus_CompleteClears(t *testing.T) {
	m := shardedMigrationTestResource(nil, false) // no external members remain
	meta.SetStatusCondition(&m.Status.Conditions, status.MigratingCondition(true, status.MigratingReasonInProgress))
	meta.SetStatusCondition(&m.Status.Conditions, status.MigrationCondition(status.MigrationPhaseConnectivityCheckPassed, "NetworkValidationPassed", "ok"))
	prev := 1
	m.Status.MigrationObservedExternalMembersCount = &prev

	m.applyComputedShardedClusterMigrationStatus(status.PhaseRunning, 11)

	assert.Nil(t, m.Status.MigrationObservedExternalMembersCount)
	assert.Nil(t, meta.FindStatusCondition(m.Status.Conditions, status.ConditionNetworkConnectivityVerified))
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionFalse, mig.Status)
	assert.Equal(t, string(status.MigratingReasonComplete), mig.Reason)
}

func TestUpdateStatus_ShardedCluster_AddingShardIsExtending(t *testing.T) {
	// Regression: adding a whole new shard (ShardCount 2 -> 3) mid-migration must report Extending.
	// The prior in-cluster count must be reconstructed with the last-reconciled shard count
	// (m.Status.ShardCount), not the just-increased spec shard count; otherwise the prior is
	// overcounted (new shard count * old per-shard distribution) and Extending is missed.
	m := shardedMigrationTestResource(oneExternalMember(), false)
	m.Spec.ShardCount = 3 // spec now wants 3 shards
	// Previous reconcile: 2 shards worth of size, and Status.ShardCount records the old topology.
	m.Status.SizeStatusInClusters = uniformSize(3, 3, 2)
	m.Status.ShardCount = 2
	prev := 1
	m.Status.MigrationObservedExternalMembersCount = &prev

	// The size option reflects the new (still-scaling) topology for this reconcile.
	m.UpdateStatus(status.PhasePending, status.ShardedClusterSizeStatusInClustersOption{SizeConfigInClusters: uniformSize(3, 3, 2)})

	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, string(status.MigratingReasonExtending), mig.Reason)
}

func TestUpdateStatus_ShardedCluster_SetsMigratingCondition(t *testing.T) {
	// Proves the ShardedCluster branch of UpdateStatus wires in the computation and applies the
	// size option before computing (dry-run => Validating regardless of counts).
	m := shardedMigrationTestResource(oneExternalMember(), true)
	m.UpdateStatus(status.PhasePending, status.ShardedClusterSizeStatusInClustersOption{SizeConfigInClusters: uniformSize(3, 3, 2)})
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig, "ShardedCluster UpdateStatus must compute the Migrating condition")
	assert.Equal(t, string(status.MigratingReasonValidating), mig.Reason)
	require.NotNil(t, m.Status.SizeStatusInClusters)
	assert.Equal(t, 11, shardedClusterInClusterMemberCount(m.Spec.ShardCount, m.Status.SizeStatusInClusters))
}

// migrationJustFinishedInSpec returns a ShardedCluster whose last external member has just been
// removed from the spec, with the status still carrying an in-flight migration from the previous
// reconcile.
func migrationJustFinishedInSpec() *MongoDB {
	m := shardedMigrationTestResource(nil, false)
	meta.SetStatusCondition(&m.Status.Conditions, status.MigratingCondition(true, status.MigratingReasonPruning))
	meta.SetStatusCondition(&m.Status.Conditions, status.MigrationCondition(
		status.MigrationPhaseConnectivityCheckPassed, "NetworkValidationPassed", "ok"))
	prev := 1
	m.Status.MigrationObservedExternalMembersCount = &prev
	return m
}

func TestUpdateStatus_ShardedCluster_DoesNotCompleteBeforeRunning(t *testing.T) {
	// spec.externalMembers is empty, but this reconcile has not succeeded: the operator is still
	// pulling those VM processes out of the automation config. Reporting MigrationComplete here
	// would tell the user the migration finished while it is still running.
	m := migrationJustFinishedInSpec()

	m.UpdateStatus(status.PhasePending)

	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionTrue, mig.Status, "migration must still read as active until the reconcile succeeds")
	assert.Equal(t, string(status.MigratingReasonPruning), mig.Reason,
		"the prior reason must be left as-is, not recomputed from a now-empty spec")
	assert.NotNil(t, meta.FindStatusCondition(m.Status.Conditions, status.ConditionNetworkConnectivityVerified),
		"connectivity verification must not be torn down before the migration actually completes")
	require.NotNil(t, m.Status.MigrationObservedExternalMembersCount)
	assert.Equal(t, 1, *m.Status.MigrationObservedExternalMembersCount)
}

func TestUpdateStatus_ShardedCluster_DoesNotCompleteOnFailed(t *testing.T) {
	m := migrationJustFinishedInSpec()

	m.UpdateStatus(status.PhaseFailed)

	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionTrue, mig.Status, "a failed reconcile must not report the migration as complete")
	assert.Equal(t, string(status.MigratingReasonPruning), mig.Reason)
}

func TestUpdateStatus_ShardedCluster_CompletesOnRunning(t *testing.T) {
	m := migrationJustFinishedInSpec()

	m.UpdateStatus(status.PhaseRunning)

	assert.Nil(t, m.Status.MigrationObservedExternalMembersCount)
	assert.Nil(t, meta.FindStatusCondition(m.Status.Conditions, status.ConditionNetworkConnectivityVerified))
	mig := meta.FindStatusCondition(m.Status.Conditions, status.ConditionMigrating)
	require.NotNil(t, mig)
	assert.Equal(t, metav1.ConditionFalse, mig.Status)
	assert.Equal(t, string(status.MigratingReasonComplete), mig.Reason)
}
