package memberwatch

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
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
	}

	for _, tt := range tests {
		assert.Equal(t, tt.out, distributeFailedMemebers(tt.inp, tt.clustername))
	}

}
