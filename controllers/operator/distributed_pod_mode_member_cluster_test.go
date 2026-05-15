package operator

// G'5 iter 17b — distributed pod mode: skip cross-cluster client init.
//
// In distributed pod-mode, the operator runs inside a member cluster Pod
// (not the central operator cluster). It must NOT register controller-runtime
// runtime clusters for peer cluster contexts: the central kubeconfig stored
// in the chart-mounted Secret encodes 127.0.0.1:<kind-loopback-port> URLs that
// are devcontainer-network artefacts and are unreachable from pod-network.
// Cache-sync against unreachable contexts times out and the manager shuts down,
// CrashLoopBackOff.
//
// The architectural invariant is:
//   1. The operator KNOWS the peer cluster names (for FSM peer list,
//      deploymentState rehydration, resource-agreement reporting).
//   2. The operator NEVER makes cross-cluster K8s API calls. It only reads
//      and writes its OWN cluster's K8s API.
//
// This file pins the controller-side half at the unit level:
//   - createMemberClusterListFromClusterSpecList must produce a MemberCluster
//     entry for every spec cluster name even when its globalMemberClustersMap
//     client is nil (= "name known, no client").
//   - Nil-client entries must report Healthy=false and have Client==nil.
//   - kubernetesClient.NewClient(nil) must NOT be called on those entries
//     (otherwise the resulting client wraps a nil interface in a non-nil
//     wrapper struct and crashes on first method call — the failure mode
//     iter-17a hit in production logs).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// TestDistributedPodMode_NilClientForPeerClusters_AppDBHelper ensures the
// shared helper used by the AppDB / Sharded / OpsMgr controllers handles a
// globalMemberClustersMap whose non-local entries map to nil clients.
//
// Scenario: distributed pod mode operator running ON kind-e2e-cluster-1 with
// spec referencing 3 clusters. Its globalMemberClustersMap has a real client
// for cluster-1 and nil for clusters 2 and 3.
//
// Invariants the helper must respect:
//   1. memberClusters has exactly 3 entries (one per spec item).
//   2. The entry for the local cluster has a non-nil Client and Healthy=true.
//   3. Entries for peer clusters have Client==nil and Healthy=false.
//   4. No nil-deref (the test simply running to completion proves it).
func TestDistributedPodMode_NilClientForPeerClusters_AppDBHelper(t *testing.T) {
	const (
		localCluster = "kind-e2e-cluster-1"
		peerCluster2 = "kind-e2e-cluster-2"
		peerCluster3 = "kind-e2e-cluster-3"
	)

	localKube := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())

	globalMap := map[string]client.Client{
		localCluster: localKube,
		peerCluster2: nil,
		peerCluster3: nil,
	}

	spec := mdbv1.ClusterSpecList{
		{ClusterName: localCluster, Members: 3},
		{ClusterName: peerCluster2, Members: 3},
		{ClusterName: peerCluster3, Members: 3},
	}

	mapping := map[string]int{
		localCluster: 0,
		peerCluster2: 1,
		peerCluster3: 2,
	}

	lastApplied := func(name string) int { return 3 }

	got := createMemberClusterListFromClusterSpecList(spec, globalMap, zap.S(), mapping, lastApplied, false)

	if assert.Len(t, got, 3, "should produce one MemberCluster per spec entry, even with nil peer clients") {
		byName := map[string]multicluster.MemberCluster{}
		for _, mc := range got {
			byName[mc.Name] = mc
		}

		assert.NotNil(t, byName[localCluster].Client, "local cluster must have a non-nil client")
		assert.True(t, byName[localCluster].Healthy, "local cluster must be Healthy")

		// The two peer entries MUST report Client==nil and Healthy=false.
		// Crucially this means createMemberClusterListFromClusterSpecList did NOT
		// call kubernetesClient.NewClient(nil) — that would wrap nil in a
		// non-nil wrapper struct (Client field nil but the wrapper itself
		// non-nil), and any later method call would crash.
		assert.Nil(t, byName[peerCluster2].Client, "peer cluster-2 must have nil Client")
		assert.False(t, byName[peerCluster2].Healthy, "peer cluster-2 must NOT be Healthy")

		assert.Nil(t, byName[peerCluster3].Client, "peer cluster-3 must have nil Client")
		assert.False(t, byName[peerCluster3].Healthy, "peer cluster-3 must NOT be Healthy")
	}
}

// TestDistributedPodMode_NilClientForPeerClusters_PreviousMembers covers the
// "previous member" branch of createMemberClusterListFromClusterSpecList —
// the section that handles clusters present in the deployment state mapping
// but absent from the current clusterSpecList (the scale-down-to-0 path).
//
// Same invariants as above: a nil entry in globalMemberClustersMap must
// produce a non-Healthy MemberCluster with Client==nil, not a non-nil
// wrapper around nil that crashes on first method call.
func TestDistributedPodMode_NilClientForPeerClusters_PreviousMembers(t *testing.T) {
	const (
		localCluster   = "kind-e2e-cluster-1"
		previousMember = "kind-e2e-cluster-2"
	)

	localKube := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())

	globalMap := map[string]client.Client{
		localCluster:   localKube,
		previousMember: nil,
	}

	// Spec only references local. previousMember is in the mapping with
	// last-applied replicas > 0, so the "previous member" branch fires.
	spec := mdbv1.ClusterSpecList{
		{ClusterName: localCluster, Members: 3},
	}

	mapping := map[string]int{
		localCluster:   0,
		previousMember: 1,
	}

	lastApplied := func(name string) int {
		if name == previousMember {
			return 2
		}
		return 3
	}

	got := createMemberClusterListFromClusterSpecList(spec, globalMap, zap.S(), mapping, lastApplied, false)

	if assert.Len(t, got, 2, "should include the previous-member entry with last-applied > 0") {
		byName := map[string]multicluster.MemberCluster{}
		for _, mc := range got {
			byName[mc.Name] = mc
		}

		assert.Nil(t, byName[previousMember].Client, "previous-member peer must have nil Client")
		assert.False(t, byName[previousMember].Healthy, "previous-member peer must NOT be Healthy")
		assert.False(t, byName[previousMember].Active, "previous-member is not Active")
	}
}
