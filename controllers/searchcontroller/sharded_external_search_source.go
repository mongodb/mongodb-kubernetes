package searchcontroller

import (
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

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

func (r *ShardedExternalSearchSource) Validate() error {
	if r.spec.Sharded == nil {
		return xerrors.New("sharded configuration is required for ShardedExternalSearchSource")
	}

	if r.spec.Sharded.MongosHostAndPort == "" {
		return xerrors.New("mongosHostAndPort is required for sharded external source")
	}

	if len(r.spec.Sharded.Shards) == 0 {
		return xerrors.New("at least one shard must be configured")
	}

	for i, shard := range r.spec.Sharded.Shards {
		if shard.Name == "" {
			return xerrors.Errorf("shard[%d].name is required", i)
		}
		if len(shard.HostAndPorts) == 0 {
			return xerrors.Errorf("shard[%d].hostAndPorts must have at least one host", i)
		}
	}

	return nil
}

func (r *ShardedExternalSearchSource) TLSConfig() *TLSSourceConfig {
	if r.spec.TLS == nil {
		return nil
	}

	return &TLSSourceConfig{
		CAFileName: "ca.crt",
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

func (r *ShardedExternalSearchSource) HostSeeds() []string {
	if r.spec.Sharded != nil && len(r.spec.Sharded.Shards) > 0 {
		return r.spec.Sharded.Shards[0].HostAndPorts
	}
	return nil
}

func (r *ShardedExternalSearchSource) GetShardCount() int {
	if r.spec.Sharded == nil {
		return 0
	}
	return len(r.spec.Sharded.Shards)
}

func (r *ShardedExternalSearchSource) GetShardNames() []string {
	if r.spec.Sharded == nil {
		return nil
	}
	names := make([]string, len(r.spec.Sharded.Shards))
	for i, shard := range r.spec.Sharded.Shards {
		names[i] = shard.Name
	}
	return names
}

func (r *ShardedExternalSearchSource) HostSeedsForShard(shardIdx int) []string {
	if r.spec.Sharded == nil || shardIdx < 0 || shardIdx >= len(r.spec.Sharded.Shards) {
		return nil
	}
	return r.spec.Sharded.Shards[shardIdx].HostAndPorts
}

func (r *ShardedExternalSearchSource) MongosHostAndPort() string {
	if r.spec.Sharded == nil {
		return ""
	}
	return r.spec.Sharded.MongosHostAndPort
}
