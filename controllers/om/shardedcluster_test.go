package om

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShardedClusterMergeFrom_RemovedShardsDeterministicOrder(t *testing.T) {
	configRs := createConfigSrvRs("configSrv", false)
	operatorCluster := NewShardedCluster("cluster", configRs.Rs.Name(), createSpecificNumberOfShards(3, "cluster"), nil)

	expectedRemoved := []string{"cluster-3", "cluster-4"}
	for range 20 {
		// mergeFrom mutates the receiver, so each iteration rebuilds the OM side fixture
		omCluster := NewShardedCluster("cluster", configRs.Rs.Name(), createSpecificNumberOfShards(5, "cluster"), nil)
		require.Equal(t, expectedRemoved, omCluster.mergeFrom(operatorCluster))
	}
}
