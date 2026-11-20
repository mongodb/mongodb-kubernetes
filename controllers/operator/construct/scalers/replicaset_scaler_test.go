package scalers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/scale"
)

func TestMultiClusterReplicaSetScaler_ExternalMembers(t *testing.T) {
	clusterSpecList := mdbv1.ClusterSpecList{{ClusterName: "cluster-1", Members: 3}}

	t.Run("scales up all at once from zero without external members", func(t *testing.T) {
		scaler := NewMultiClusterReplicaSetScaler("rs", clusterSpecList, "cluster-1", 0,
			[]multicluster.MemberCluster{{Name: "cluster-1", Index: 0, Replicas: 0}}, false)
		assert.False(t, scaler.ForcedIndividualScaling())
		assert.Equal(t, 3, scale.ReplicasThisReconciliation(scaler))
	})

	t.Run("scales up one member at a time from zero with external members", func(t *testing.T) {
		// During migration the replica set already exists on the external (VM) members, so zero
		// Kubernetes members must not trigger the first-time shortcut — MongoDB forbids adding more
		// than one voting member to an existing replica set in a single reconfiguration.
		scaler := NewMultiClusterReplicaSetScaler("rs", clusterSpecList, "cluster-1", 0,
			[]multicluster.MemberCluster{{Name: "cluster-1", Index: 0, Replicas: 0}}, true)
		assert.True(t, scaler.ForcedIndividualScaling())
		assert.Equal(t, 1, scale.ReplicasThisReconciliation(scaler))
	})

	t.Run("scales up one member at a time from non-zero with external members", func(t *testing.T) {
		scaler := NewMultiClusterReplicaSetScaler("rs", clusterSpecList, "cluster-1", 0,
			[]multicluster.MemberCluster{{Name: "cluster-1", Index: 0, Replicas: 1}}, true)
		assert.True(t, scaler.ForcedIndividualScaling())
		assert.Equal(t, 2, scale.ReplicasThisReconciliation(scaler))
	})

	t.Run("external members do not affect a replica set already at target", func(t *testing.T) {
		scaler := NewMultiClusterReplicaSetScaler("rs", clusterSpecList, "cluster-1", 0,
			[]multicluster.MemberCluster{{Name: "cluster-1", Index: 0, Replicas: 3}}, true)
		assert.Equal(t, 3, scale.ReplicasThisReconciliation(scaler))
	})
}
