package memberwatch

import (
	"encoding/json"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster/failedcluster"
	"github.com/stretchr/testify/assert"
)

func TestClusterWithMinimumMmber(t *testing.T) {
	tests := []struct {
		inp []mdbmulti.ClusterSpecItem
		out int
	}{
		{
			inp: []mdbmulti.ClusterSpecItem{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			out: 1,
		},
		{
			inp: []mdbmulti.ClusterSpecItem{
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
		inp         []mdbmulti.ClusterSpecItem
		clustername string
		out         []mdbmulti.ClusterSpecItem
	}{
		{
			inp: []mdbmulti.ClusterSpecItem{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clustername: "cluster1",
			out: []mdbmulti.ClusterSpecItem{
				{ClusterName: "cluster2", Members: 2},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 2},
			},
		},
		{
			inp: []mdbmulti.ClusterSpecItem{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clustername: "cluster2",
			out: []mdbmulti.ClusterSpecItem{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 2},
			},
		},
		{
			inp: []mdbmulti.ClusterSpecItem{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clustername: "cluster3",
			out: []mdbmulti.ClusterSpecItem{
				{ClusterName: "cluster1", Members: 3},
				{ClusterName: "cluster2", Members: 3},
				{ClusterName: "cluster4", Members: 2},
			},
		},
		{
			inp: []mdbmulti.ClusterSpecItem{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clustername: "cluster4",
			out: []mdbmulti.ClusterSpecItem{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 2},
				{ClusterName: "cluster3", Members: 4},
			},
		},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.out, distributeFailedMembers(tt.inp, tt.clustername))
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
		clustername string
		out         bool
	}{
		{
			annotations: nil,
			clustername: "cluster1",
			out:         true,
		},
		{
			annotations: map[string]string{failedcluster.FailedClusterAnnotation: getFailedClusterList([]string{"cluster1", "cluster2"})},
			clustername: "cluster1",
			out:         false,
		},
		{
			annotations: map[string]string{failedcluster.FailedClusterAnnotation: getFailedClusterList([]string{"cluster1", "cluster2", "cluster4"})},
			clustername: "cluster3",
			out:         true,
		},
	}

	for _, tt := range tests {
		assert.Equal(t, shouldAddFailedClusterAnnotation(tt.annotations, tt.clustername), tt.out)
	}
}
