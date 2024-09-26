package memberwatch

import (
	"encoding/json"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster/failedcluster"
	"github.com/stretchr/testify/assert"
)

func TestClusterWithMinimumNumber(t *testing.T) {
	tests := []struct {
		inp mdb.ClusterSpecList
		out int
	}{
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			out: 1,
		},
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 1},
				{ClusterName: "cluster2", Members: 2},
				{ClusterName: "cluster3", Members: 3},
				{ClusterName: "cluster4", Members: 4},
			},
			out: 0,
		},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.out, clusterWithMinimumMembers(tt.inp))
	}
}

func TestDistributeFailedMembers(t *testing.T) {
	tests := []struct {
		inp         mdb.ClusterSpecList
		clusterName string
		out         mdb.ClusterSpecList
	}{
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clusterName: "cluster1",
			out: mdb.ClusterSpecList{
				{ClusterName: "cluster2", Members: 2},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 2},
			},
		},
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clusterName: "cluster2",
			out: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 2},
			},
		},
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clusterName: "cluster3",
			out: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 3},
				{ClusterName: "cluster2", Members: 3},
				{ClusterName: "cluster4", Members: 2},
			},
		},
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clusterName: "cluster4",
			out: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 2},
				{ClusterName: "cluster3", Members: 4},
			},
		},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.out, distributeFailedMembers(tt.inp, tt.clusterName))
	}
}

func getFailedClusterList(clusters []string) string {
	failedClusters := make([]failedcluster.FailedCluster, len(clusters))

	for n, c := range clusters {
		failedClusters[n] = failedcluster.FailedCluster{ClusterName: c, Members: 2}
	}

	failedClusterBytes, _ := json.Marshal(failedClusters)
	return string(failedClusterBytes)
}

func TestShouldAddFailedClusterAnnotation(t *testing.T) {
	tests := []struct {
		annotations map[string]string
		clusterName string
		out         bool
	}{
		{
			annotations: nil,
			clusterName: "cluster1",
			out:         true,
		},
		{
			annotations: map[string]string{failedcluster.FailedClusterAnnotation: getFailedClusterList([]string{"cluster1", "cluster2"})},
			clusterName: "cluster1",
			out:         false,
		},
		{
			annotations: map[string]string{failedcluster.FailedClusterAnnotation: getFailedClusterList([]string{"cluster1", "cluster2", "cluster4"})},
			clusterName: "cluster3",
			out:         true,
		},
	}

	for _, tt := range tests {
		assert.Equal(t, shouldAddFailedClusterAnnotation(tt.annotations, tt.clusterName), tt.out)
	}
}
