package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

func TestWorstOfPhase(t *testing.T) {
	tests := []struct {
		name   string
		phases []status.Phase
		want   status.Phase
	}{
		{name: "empty", phases: nil, want: ""},
		{name: "single Running", phases: []status.Phase{status.PhaseRunning}, want: status.PhaseRunning},
		{name: "all Running", phases: []status.Phase{status.PhaseRunning, status.PhaseRunning}, want: status.PhaseRunning},
		{name: "Pending beats Running", phases: []status.Phase{status.PhaseRunning, status.PhasePending}, want: status.PhasePending},
		{name: "Failed beats Pending", phases: []status.Phase{status.PhasePending, status.PhaseFailed}, want: status.PhaseFailed},
		{name: "Failed beats Running", phases: []status.Phase{status.PhaseRunning, status.PhaseFailed, status.PhaseRunning}, want: status.PhaseFailed},
		{name: "unknown phase skipped", phases: []status.Phase{status.Phase("Mystery"), status.PhaseRunning}, want: status.PhaseRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, WorstOfPhase(tt.phases...))
		})
	}
}

func TestAggregateClusterStatuses_Legacy_NoOp(t *testing.T) {
	s := &MongoDBSearch{}
	s.Status.Phase = status.PhaseRunning
	// No per-cluster items provided → top-level Phase preserved, list stays empty.
	s.AggregateClusterStatuses(nil)
	assert.Equal(t, status.PhaseRunning, s.Status.Phase)
	assert.Empty(t, s.Status.ClusterStatusList.ClusterStatuses)
}

func TestAggregateClusterStatuses_MultiCluster_AllRunning(t *testing.T) {
	s := &MongoDBSearch{}
	items := []SearchClusterStatusItem{
		{ClusterName: "us-east-k8s", Common: status.Common{Phase: status.PhaseRunning}, ObservedReplicas: 2},
		{ClusterName: "eu-west-k8s", Common: status.Common{Phase: status.PhaseRunning}, ObservedReplicas: 2},
	}
	s.AggregateClusterStatuses(items)
	assert.Equal(t, status.PhaseRunning, s.Status.Phase)
	require.Len(t, s.Status.ClusterStatusList.ClusterStatuses, 2)
	assert.Equal(t, "us-east-k8s", s.Status.ClusterStatusList.ClusterStatuses[0].ClusterName)
	assert.Equal(t, int32(2), s.Status.ClusterStatusList.ClusterStatuses[0].ObservedReplicas)
}

func TestAggregateClusterStatuses_WorstOfFlipsTopLevel(t *testing.T) {
	s := &MongoDBSearch{}
	items := []SearchClusterStatusItem{
		{ClusterName: "us-east-k8s", Common: status.Common{Phase: status.PhaseRunning}},
		{ClusterName: "eu-west-k8s", Common: status.Common{Phase: status.PhasePending, Message: "secret missing"}},
	}
	s.AggregateClusterStatuses(items)
	assert.Equal(t, status.PhasePending, s.Status.Phase, "Pending beats Running")
	require.Len(t, s.Status.ClusterStatusList.ClusterStatuses, 2)
	assert.Equal(t, status.PhasePending, s.Status.ClusterStatusList.ClusterStatuses[1].Phase)
}

func TestAggregateClusterStatuses_FailedDominates(t *testing.T) {
	s := &MongoDBSearch{}
	items := []SearchClusterStatusItem{
		{ClusterName: "us-east-k8s", Common: status.Common{Phase: status.PhaseRunning}},
		{ClusterName: "eu-west-k8s", Common: status.Common{Phase: status.PhasePending}},
		{ClusterName: "ap-south-k8s", Common: status.Common{Phase: status.PhaseFailed}},
	}
	s.AggregateClusterStatuses(items)
	assert.Equal(t, status.PhaseFailed, s.Status.Phase)
}

func TestAggregateClusterStatuses_WarningsAndLoadBalancerCarry(t *testing.T) {
	s := &MongoDBSearch{}
	items := []SearchClusterStatusItem{
		{
			ClusterName:      "us-east-k8s",
			Common:           status.Common{Phase: status.PhaseRunning},
			ObservedReplicas: 2,
			Warnings:         []status.Warning{"B5: secret 'search-sync-password' missing"},
			LoadBalancer:     &LoadBalancerStatus{Phase: status.PhaseRunning},
		},
	}
	s.AggregateClusterStatuses(items)
	require.Len(t, s.Status.ClusterStatusList.ClusterStatuses, 1)
	assert.Equal(t, []status.Warning{"B5: secret 'search-sync-password' missing"}, s.Status.ClusterStatusList.ClusterStatuses[0].Warnings)
	assert.NotNil(t, s.Status.ClusterStatusList.ClusterStatuses[0].LoadBalancer)
}
