package searchcontroller

import (
	"fmt"
	"slices"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

// ShardedExternalSearchSource implements ShardedSearchSourceDBResource for external sharded MongoDB clusters.
type ShardedExternalSearchSource struct {
	namespace string
	spec      *searchv1.ExternalMongoDBSource
}

func NewShardedExternalSearchSource(namespace string, spec *searchv1.ExternalMongoDBSource) *ShardedExternalSearchSource {
	return &ShardedExternalSearchSource{
		namespace: namespace,
		spec:      spec,
	}
}

func (r *ShardedExternalSearchSource) ResourceType() mdbv1.ResourceType {
	return mdbv1.ShardedCluster
}

func (r *ShardedExternalSearchSource) Validate() error {
	if r.spec.ShardedCluster == nil {
		return xerrors.New("sharded configuration is required for ShardedExternalSearchSource")
	}

	if len(r.spec.ShardedCluster.Router.Hosts) == 0 {
		return xerrors.New("router.hosts must have at least one host")
	}

	if len(r.spec.ShardedCluster.Shards) == 0 {
		return xerrors.New("at least one shard must be configured")
	}

	seenShards := make(map[string]struct{}, len(r.spec.ShardedCluster.Shards))
	for i, shard := range r.spec.ShardedCluster.Shards {
		if err := searchv1.ValidateShardNameRFC1123(shard.ShardName); err != nil {
			return xerrors.Errorf("shard[%d]: %w", i, err)
		}

		if _, ok := seenShards[shard.ShardName]; ok {
			return xerrors.Errorf("shardNames can not be duplicate, shard name %s is duplicate", shard.ShardName)
		}
		seenShards[shard.ShardName] = struct{}{}

		if len(shard.Hosts) == 0 {
			return xerrors.Errorf("shard[%d].hosts must have at least one host", i)
		}
	}

	return nil
}

func (r *ShardedExternalSearchSource) TLSConfig() *TLSSourceConfig {
	if r.spec.TLS == nil {
		return nil
	}

	return &TLSSourceConfig{
		CAFileName: tlsCACertName,
		CAVolume:   statefulset.CreateVolumeFromSecret("ca", r.spec.TLS.CA.Name),
		ResourcesToWatch: map[watch.Type][]types.NamespacedName{
			watch.Secret: {
				{Namespace: r.namespace, Name: r.spec.TLS.CA.Name},
			},
		},
	}
}

func (r *ShardedExternalSearchSource) KeyfileSecretName() string {
	if r.spec.KeyFileSecretKeyRef != nil {
		return r.spec.KeyFileSecretKeyRef.Name
	}
	return ""
}

func (r *ShardedExternalSearchSource) GetShardCount() int {
	if r.spec.ShardedCluster == nil {
		return 0
	}
	return len(r.spec.ShardedCluster.Shards)
}

func (r *ShardedExternalSearchSource) GetShardNames() []string {
	if r.spec.ShardedCluster == nil {
		return nil
	}
	names := make([]string, len(r.spec.ShardedCluster.Shards))
	for i, shard := range r.spec.ShardedCluster.Shards {
		names[i] = shard.ShardName
	}
	return names
}

func (r *ShardedExternalSearchSource) HostSeeds(shardName string) ([]string, error) {
	if r.spec.ShardedCluster == nil {
		return nil, nil
	}

	shardIndex := slices.IndexFunc(r.spec.ShardedCluster.Shards, func(c searchv1.ExternalShardConfig) bool {
		return c.ShardName == shardName
	})
	if shardIndex == -1 {
		return nil, fmt.Errorf("shardName %s not found in external sharded cluster configuration", shardName)
	}

	return r.spec.ShardedCluster.Shards[shardIndex].Hosts, nil
}

func (r *ShardedExternalSearchSource) MongosHostsAndPorts() []string {
	if r.spec.ShardedCluster == nil || len(r.spec.ShardedCluster.Router.Hosts) == 0 {
		return nil
	}
	return r.spec.ShardedCluster.Router.Hosts
}

// GetUnmanagedLBEndpointForShard returns an empty string for external sharded sources
// since unmanaged LB configuration is not applicable - the operator is not managing neither LB nor MongoDB cluster.
func (r *ShardedExternalSearchSource) GetUnmanagedLBEndpointForShard(shardName string) string {
	return ""
}
