package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

func TestGetReplicasNilDefaultsToOne(t *testing.T) {
	s := &MongoDBSearch{}
	assert.Equal(t, 1, s.GetReplicas())
}

func TestGetReplicasReturnsExplicitValue(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(3))}}
	assert.Equal(t, 3, s.GetReplicas())
}

func TestHasMultipleReplicas(t *testing.T) {
	assert.False(t, (&MongoDBSearch{}).HasMultipleReplicas())
	assert.False(t, (&MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(1))}}).HasMultipleReplicas())
	assert.True(t, (&MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(2))}}).HasMultipleReplicas())
}

func TestUpdateStatus_MainPath(t *testing.T) {
	s := &MongoDBSearch{}
	s.UpdateStatus(status.PhaseRunning, NewMongoDBSearchVersionOption("1.0"))

	assert.Equal(t, status.PhaseRunning, s.Status.Phase)
	assert.Equal(t, "1.0", s.Status.Version)
	assert.Nil(t, s.Status.LoadBalancer)
}

func TestUpdateStatus_LoadBalancerPath(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)
	s.UpdateStatus(status.PhaseRunning, partOpt, status.NewMessageOption("Envoy ready"))

	assert.NotNil(t, s.Status.LoadBalancer)
	assert.Equal(t, status.PhaseRunning, s.Status.LoadBalancer.Phase)
	assert.Equal(t, "Envoy ready", s.Status.LoadBalancer.Message)
	// Main status untouched
	assert.Equal(t, status.Phase(""), s.Status.Phase)
}

func TestGetStatusPath_Default(t *testing.T) {
	s := &MongoDBSearch{}
	assert.Equal(t, "/status", s.GetStatusPath())
}

func TestGetStatusPath_LoadBalancer(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)
	assert.Equal(t, "/status/loadBalancer", s.GetStatusPath(partOpt))
}

func TestGetStatus_Default(t *testing.T) {
	s := &MongoDBSearch{}
	s.Status.Phase = status.PhaseRunning
	got := s.GetStatus()
	assert.Equal(t, s.Status, got)
}

func TestGetStatus_LoadBalancer(t *testing.T) {
	s := &MongoDBSearch{}
	s.Status.LoadBalancer = &LoadBalancerStatus{Phase: status.PhaseRunning, Message: "ok"}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)
	got := s.GetStatus(partOpt)
	assert.Equal(t, s.Status.LoadBalancer, got)
}

// nil LB → GetStatus returns nil, used by clearLBStatus to patch null
func TestGetStatus_LoadBalancerNil(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)
	got := s.GetStatus(partOpt)
	assert.Nil(t, got)
}

// Failed→Running transition must not carry over the old error message
func TestUpdateStatus_LoadBalancerClearsStaleMessage(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)

	// Simulate a failure with a message
	s.UpdateStatus(status.PhaseFailed, partOpt, status.NewMessageOption("missing image"))
	assert.Equal(t, "missing image", s.Status.LoadBalancer.Message)

	// Transition to Running without a message — old message must be cleared
	s.UpdateStatus(status.PhaseRunning, partOpt)
	assert.Equal(t, status.PhaseRunning, s.Status.LoadBalancer.Phase)
	assert.Equal(t, "", s.Status.LoadBalancer.Message)
}

func TestIsLoadBalancerReady(t *testing.T) {
	tests := []struct {
		name     string
		search   MongoDBSearch
		expected bool
	}{
		{
			name:     "no LB configured",
			search:   MongoDBSearch{},
			expected: true,
		},
		{
			name: "managed LB, status nil",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}},
				},
			},
			expected: false,
		},
		{
			name: "managed LB, status Running",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}},
				},
				Status: MongoDBSearchStatus{
					LoadBalancer: &LoadBalancerStatus{Phase: status.PhaseRunning},
				},
			},
			expected: true,
		},
		{
			name: "managed LB, status Pending",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}},
				},
				Status: MongoDBSearchStatus{
					LoadBalancer: &LoadBalancerStatus{Phase: status.PhasePending},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.search.IsLoadBalancerReady())
		})
	}
}
