package operator

import (
	"context"
	"fmt"
	"reflect"

	"go.uber.org/zap"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

// ClusterMappingProvider resolves the clusterName→clusterIndex mapping for a
// MongoDBSearch reconcile. The implementation is chosen once at startup in
// main.go (StateCMProvider for legacy single/central-MC, SpecIndexProvider for
// simulated multi-cluster) so the reconcile body never branches on mode.
type ClusterMappingProvider interface {
	Resolve(ctx context.Context, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (MappingResolution, error)
}

// MappingResolution is the outcome of a provider Resolve call.
// Skip == true means "this CR is not for this operator instance" — the
// reconciler returns reconcile.Result{} immediately and does not touch status.
// When Skip == false, Mapping is always non-nil (empty allowed).
type MappingResolution struct {
	Mapping map[string]int
	Skip    bool
}

// StateCMProvider implements the legacy central-multicluster mapping path.
// It loads the per-CR {search-name}-state ConfigMap, applies
// AssignClusterIndices against the current spec.clusters[], and writes back
// when the mapping changed.
type StateCMProvider struct {
	central kubernetesClient.Client
}

func NewStateCMProvider(central kubernetesClient.Client) *StateCMProvider {
	return &StateCMProvider{central: central}
}

func (p *StateCMProvider) Resolve(ctx context.Context, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (MappingResolution, error) {
	state, store, err := loadOrInitSearchState(ctx, p.central, search)
	if err != nil {
		return MappingResolution{}, err
	}
	var current []searchv1.ClusterSpec
	if search.Spec.Clusters != nil {
		current = *search.Spec.Clusters
	}
	newMapping := searchv1.AssignClusterIndices(state.ClusterMapping, current)
	if !reflect.DeepEqual(newMapping, state.ClusterMapping) {
		state.ClusterMapping = newMapping
		if err := store.WriteState(ctx, state, log); err != nil {
			return MappingResolution{}, err
		}
	}
	return MappingResolution{Mapping: state.ClusterMapping}, nil
}

// SpecIndexProvider implements the simulated multi-cluster mapping path: one
// operator per Kubernetes cluster, each reconciling the SAME CR but acting
// only on its own spec.clusters[i] entry. It mutates search.Spec.Clusters in
// place to a 1-element slice containing the local entry, and synthesises the
// mapping from spec.clusters[i].ClusterIndex (no state ConfigMap).
//
// v0 constraints:
//   - replica-set external source only (sharded source rejected)
//   - every spec.clusters[i].clusterIndex MUST be set
//
// When the operator's cluster name is not present in spec.clusters[],
// Resolve returns {Skip: true} (silent no-op; CR belongs to another operator).
type SpecIndexProvider struct {
	operatorClusterName string
}

func NewSpecIndexProvider(operatorClusterName string) *SpecIndexProvider {
	return &SpecIndexProvider{operatorClusterName: operatorClusterName}
}

func (p *SpecIndexProvider) Resolve(_ context.Context, search *searchv1.MongoDBSearch, _ *zap.SugaredLogger) (MappingResolution, error) {
	// Degenerate single-cluster CR seen by a simulated-MC operator: legacy
	// behaviour (no projection, empty mapping).
	if search.Spec.Clusters == nil || len(*search.Spec.Clusters) == 0 {
		return MappingResolution{Mapping: map[string]int{}}, nil
	}
	if search.IsExternalSourceSharded() {
		return MappingResolution{}, fmt.Errorf("simulated multi-cluster mode v0 supports replica-set source only; spec.source.external.shardedCluster is not supported")
	}
	clusters := *search.Spec.Clusters
	matchIdx := -1
	for i := range clusters {
		if clusters[i].ClusterIndex == nil {
			return MappingResolution{}, fmt.Errorf("simulated multi-cluster mode requires spec.clusters[%d].clusterIndex to be set (cluster %q)", i, clusters[i].ClusterName)
		}
		if clusters[i].ClusterName == p.operatorClusterName {
			matchIdx = i
		}
	}
	if matchIdx == -1 {
		return MappingResolution{Skip: true}, nil
	}
	matched := clusters[matchIdx]
	only := []searchv1.ClusterSpec{matched}
	search.Spec.Clusters = &only
	return MappingResolution{Mapping: map[string]int{p.operatorClusterName: int(*matched.ClusterIndex)}}, nil
}
