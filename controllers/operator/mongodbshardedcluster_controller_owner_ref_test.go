package operator

// G iter-17d — STS OwnerReferences gating for distributed multi-cluster mode.
//
// Diagnostic context: under the hub-spoke takeover-to-distributed protocol,
// do_distributed_pre_replicate creates a fresh MongoDB CR on each member
// cluster, each with a new server-assigned UID different from the central
// CR's UID. The existing hub-spoke-written STSes carry ownerReferences
// pointing at the central CR's UID — which on the member cluster is an
// unresolvable owner. K8s GC then sweeps those STSes within seconds, the
// distributed operator's first reconcile sees NotFound and recreates them,
// driving full STS-uid + pod-uid churn during what is supposed to be a
// zero-disruption takeover (Phase D PoC criterion).
//
// Fix: in distributed mode (r.coordinator != nil), omit ownerReferences on
// the STS write. The distributed operator owns the STS lifecycle directly;
// the existing label-driven cleanup in ShardedClusterReconcileHelper.OnDelete
// → deleteClusterResources handles CR-delete cleanup without K8s GC. This
// test pins both branches: hub-spoke retains ownerReferences (regression
// guard), distributed mode emits an STS with zero ownerReferences.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// ownerRefTestDeploymentOpts returns a minimally-populated deploymentOptions
// sufficient for buildVaultDatabaseSecretsToInject + STS construction to
// run without nil-pointer panics. The test only inspects sts.OwnerReferences,
// so the surrounding payload need not be realistic.
func ownerRefTestDeploymentOpts() deploymentOptions {
	return deploymentOptions{
		podEnvVars: &env.PodEnvVars{ProjectID: "test-project"},
	}
}

// TestSTSOwnerReferences_HubSpoke pins the existing hub-spoke behaviour:
// every STS the helper writes carries ownerReferences pointing at the CR.
// This is the regression guard for the iter-17d fix.
func TestSTSOwnerReferences_HubSpoke(t *testing.T) {
	ctx := context.Background()
	helper, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)
	// Hub-spoke: NO coordinator attached.
	require.Nil(t, helper.coordinator, "test precondition: hub-spoke helper has no coordinator")

	log := zap.S()
	depOpts := ownerRefTestDeploymentOpts()

	// Each of the three component STS constructors must produce a STS
	// whose .OwnerReferences is non-empty and points at the CR.
	for _, shardMC := range helper.shardsMemberClustersMap[0] {
		shardOptsFn := helper.getShardOptions(ctx, *sc, 0, depOpts, log, shardMC)
		sts := construct.DatabaseStatefulSet(*sc, shardOptsFn, log)
		require.NotEmpty(t, sts.OwnerReferences,
			"hub-spoke shard STS on cluster %s: ownerReferences must be set", shardMC.Name)
		assert.Equal(t, sc.Name, sts.OwnerReferences[0].Name,
			"hub-spoke shard STS on cluster %s: ownerRef.Name must match CR", shardMC.Name)
	}

	for _, configMC := range helper.configSrvMemberClusters {
		configOptsFn := helper.getConfigServerOptions(ctx, *sc, depOpts, log, configMC)
		sts := construct.DatabaseStatefulSet(*sc, configOptsFn, log)
		require.NotEmpty(t, sts.OwnerReferences,
			"hub-spoke config STS on cluster %s: ownerReferences must be set", configMC.Name)
		assert.Equal(t, sc.Name, sts.OwnerReferences[0].Name,
			"hub-spoke config STS on cluster %s: ownerRef.Name must match CR", configMC.Name)
	}

	for _, mongosMC := range helper.mongosMemberClusters {
		mongosOptsFn := helper.getMongosOptions(ctx, *sc, depOpts, log, mongosMC)
		sts := construct.DatabaseStatefulSet(*sc, mongosOptsFn, log)
		require.NotEmpty(t, sts.OwnerReferences,
			"hub-spoke mongos STS on cluster %s: ownerReferences must be set", mongosMC.Name)
		assert.Equal(t, sc.Name, sts.OwnerReferences[0].Name,
			"hub-spoke mongos STS on cluster %s: ownerRef.Name must match CR", mongosMC.Name)
	}
}

// TestSTSOwnerReferences_DistributedMode pins the iter-17d fix: when the
// helper has a coordinator attached (distributed mode), STS construction
// emits zero ownerReferences. Cross-cluster ownerReferences are the root
// cause of the takeover STS-recreation disruption; this test fails on tip
// 81ce17d52 (pre-fix) and passes once the gate is added.
func TestSTSOwnerReferences_DistributedMode(t *testing.T) {
	ctx := context.Background()
	helper, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	// Attach a coordinator. The cluster name does not need to match a member —
	// the gate is on r.coordinator != nil, not on identity.
	helper.SetCoordinator(newFakeCoordinator("member-cluster-1", false))
	require.NotNil(t, helper.coordinator, "test precondition: distributed mode")

	log := zap.S()
	depOpts := ownerRefTestDeploymentOpts()

	for _, shardMC := range helper.shardsMemberClustersMap[0] {
		shardOptsFn := helper.getShardOptions(ctx, *sc, 0, depOpts, log, shardMC)
		sts := construct.DatabaseStatefulSet(*sc, shardOptsFn, log)
		assert.Empty(t, sts.OwnerReferences,
			"distributed-mode shard STS on cluster %s: ownerReferences must be empty (cross-cluster ownerRef triggers K8s GC under takeover)", shardMC.Name)
	}

	for _, configMC := range helper.configSrvMemberClusters {
		configOptsFn := helper.getConfigServerOptions(ctx, *sc, depOpts, log, configMC)
		sts := construct.DatabaseStatefulSet(*sc, configOptsFn, log)
		assert.Empty(t, sts.OwnerReferences,
			"distributed-mode config STS on cluster %s: ownerReferences must be empty", configMC.Name)
	}

	for _, mongosMC := range helper.mongosMemberClusters {
		mongosOptsFn := helper.getMongosOptions(ctx, *sc, depOpts, log, mongosMC)
		sts := construct.DatabaseStatefulSet(*sc, mongosOptsFn, log)
		assert.Empty(t, sts.OwnerReferences,
			"distributed-mode mongos STS on cluster %s: ownerReferences must be empty", mongosMC.Name)
	}
}
