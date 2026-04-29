package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

func newShardedInternalSearchSource(t *testing.T, version, topology string, shardCount int) *ShardedInternalSearchSource {
	t.Helper()
	mdb := &mdbv1.MongoDB{}
	mdb.Spec.ResourceType = mdbv1.ShardedCluster
	mdb.Spec.Version = version
	mdb.Spec.Topology = topology
	mdb.Spec.ShardCount = shardCount
	return NewShardedInternalSearchSource(mdb, nil)
}

func TestShardedInternalSearchSource_Validate_MultiClusterDeferral(t *testing.T) {
	src := newShardedInternalSearchSource(t, "8.2.0", mdbv1.ClusterTopologyMultiCluster, 2)
	err := src.Validate()
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "multi-cluster source (Q1-MC) is deferred to phase 2")
		assert.Contains(t, err.Error(), "internal sharded MongoDB source")
	}
}

func TestShardedInternalSearchSource_Validate_SingleClusterAccepted(t *testing.T) {
	src := newShardedInternalSearchSource(t, "8.2.0", mdbv1.ClusterTopologySingleCluster, 2)
	err := src.Validate()
	assert.NoError(t, err)
}
