package searchcontroller

import (
	"context"

	"go.uber.org/zap"
)

// MongoDBSearchDeploymentState is persisted to a <name>-state ConfigMap for each MongoDBSearch.
// It is the durable source of truth for the clusterName → clusterIndex mapping, and for any
// per-cluster bookkeeping downstream lifecycle phases need to remember between reconciles.
type MongoDBSearchDeploymentState struct {
	// ClusterMapping is a stable, append-only map of clusterName → clusterIndex.
	// Indices are assigned by AssignIndexesForMemberClusterNames and never reused.
	ClusterMapping map[string]int `json:"clusterMapping,omitempty"`
	// LastAppliedMemberSpec records the last reconciled per-cluster replica count, keyed by
	// clusterName. Phase 6 cluster-removal cleanup uses this to know what was provisioned
	// without re-deriving from the (now stale) spec. Phase 1 ships the field; nothing reads
	// it yet — included now to avoid a Phase 6 state-schema migration.
	LastAppliedMemberSpec map[string]int `json:"lastAppliedMemberSpec,omitempty"`
}

// NewMongoDBSearchDeploymentState returns an initialised empty deployment state.
func NewMongoDBSearchDeploymentState() *MongoDBSearchDeploymentState {
	return &MongoDBSearchDeploymentState{
		ClusterMapping:        map[string]int{},
		LastAppliedMemberSpec: map[string]int{},
	}
}

// deploymentStateStore abstracts ConfigMap-backed persistence for MongoDBSearchDeploymentState.
// It is satisfied by *operator.StateStore[MongoDBSearchDeploymentState], which the controller
// constructs and passes to NewMongoDBSearchReconcileHelper.
type deploymentStateStore interface {
	WriteState(ctx context.Context, state *MongoDBSearchDeploymentState, log *zap.SugaredLogger) error
	ReadState(ctx context.Context) (*MongoDBSearchDeploymentState, error)
}
