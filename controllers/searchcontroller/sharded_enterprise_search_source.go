package searchcontroller

import (
	"fmt"
	"strings"

	"github.com/blang/semver"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// ShardedEnterpriseSearchSource implements SearchSourceDBResource for sharded MongoDB clusters.
// It provides per-shard host seeds and configuration for the sharded Search + external L7 LB PoC.
//
// Sharded internal + external L7 LB (BYO per-shard LB) PoC:
// - spec.lb.mode == External
// - spec.lb.external.sharded.endpoints[].{shardName, endpoint}
// We map shardName -> endpoint and configure each mongod shard
// to use its shard-local external LB endpoint for Search gRPC.
type ShardedEnterpriseSearchSource struct {
	*mdbv1.MongoDB
	search *searchv1.MongoDBSearch
}

// NewShardedEnterpriseSearchSource creates a new ShardedEnterpriseSearchSource for a sharded MongoDB cluster.
func NewShardedEnterpriseSearchSource(mdb *mdbv1.MongoDB, search *searchv1.MongoDBSearch) *ShardedEnterpriseSearchSource {
	return &ShardedEnterpriseSearchSource{
		MongoDB: mdb,
		search:  search,
	}
}

// GetShardNames returns the list of shard names for this sharded cluster.
func (r *ShardedEnterpriseSearchSource) GetShardNames() []string {
	shardNames := make([]string, r.Spec.ShardCount)
	for i := 0; i < r.Spec.ShardCount; i++ {
		shardNames[i] = r.ShardRsName(i)
	}
	return shardNames
}

// GetShardCount returns the number of shards in this cluster.
func (r *ShardedEnterpriseSearchSource) GetShardCount() int {
	return r.Spec.ShardCount
}

// HostSeedsForShard returns the host seeds for a specific shard.
// These are the mongod hosts that mongot will connect to for data synchronization.
func (r *ShardedEnterpriseSearchSource) HostSeedsForShard(shardIdx int) []string {
	shardName := r.ShardRsName(shardIdx)
	members := r.Spec.MongodsPerShardCount
	clusterDomain := r.Spec.GetClusterDomain()
	port := r.Spec.GetAdditionalMongodConfig().GetPortOrDefault()

	seeds := make([]string, members)
	for i := 0; i < members; i++ {
		// Format: <shardName>-<memberIdx>.<shardServiceName>.<namespace>.svc.<clusterDomain>:<port>
		seeds[i] = fmt.Sprintf("%s-%d.%s.%s.svc.%s:%d",
			shardName, i, r.ShardServiceName(), r.Namespace, clusterDomain, port)
	}
	return seeds
}

// HostSeeds returns the host seeds for the first shard (for backward compatibility).
// For sharded clusters, use HostSeedsForShard instead.
func (r *ShardedEnterpriseSearchSource) HostSeeds() []string {
	if r.Spec.ShardCount > 0 {
		return r.HostSeedsForShard(0)
	}
	return nil
}

// MongosHostAndPort returns the mongos host:port for the sharded cluster.
// This is used for the router section in mongot config.
func (r *ShardedEnterpriseSearchSource) MongosHostAndPort() string {
	clusterDomain := r.Spec.GetClusterDomain()
	port := r.Spec.GetAdditionalMongodConfig().GetPortOrDefault()
	// Format: <serviceName>.<namespace>.svc.<clusterDomain>:<port>
	return fmt.Sprintf("%s.%s.svc.%s:%d", r.ServiceName(), r.Namespace, clusterDomain, port)
}

// TLSConfig returns the TLS configuration for the sharded cluster.
func (r *ShardedEnterpriseSearchSource) TLSConfig() *TLSSourceConfig {
	if !r.Spec.Security.IsTLSEnabled() {
		return nil
	}

	return &TLSSourceConfig{
		CAFileName: "ca-pem",
		CAVolume:   statefulset.CreateVolumeFromConfigMap("ca", r.Spec.Security.TLSConfig.CA),
		ResourcesToWatch: map[watch.Type][]types.NamespacedName{
			watch.ConfigMap: {
				{Namespace: r.Namespace, Name: r.Spec.Security.TLSConfig.CA},
			},
		},
	}
}

// KeyfileSecretName returns the keyfile secret name for the sharded cluster.
func (r *ShardedEnterpriseSearchSource) KeyfileSecretName() string {
	return fmt.Sprintf("%s-%s", r.Name, MongotKeyfileFilename)
}

// Validate validates that the MongoDB resource is suitable for sharded Search.
func (r *ShardedEnterpriseSearchSource) Validate() error {
	version, err := semver.ParseTolerant(util.StripEnt(r.Spec.GetMongoDBVersion()))
	if err != nil {
		return xerrors.Errorf("error parsing MongoDB version '%s': %w", r.Spec.GetMongoDBVersion(), err)
	} else if version.LT(semver.MustParse("8.2.0")) {
		return xerrors.New("MongoDB version must be 8.2.0 or higher")
	}

	if r.Spec.GetTopology() != mdbv1.ClusterTopologySingleCluster {
		return xerrors.Errorf("MongoDBSearch for sharded clusters is only supported for %s topology", mdbv1.ClusterTopologySingleCluster)
	}

	if r.GetResourceType() != mdbv1.ShardedCluster {
		return xerrors.Errorf("ShardedEnterpriseSearchSource requires a %s resource, got %s", mdbv1.ShardedCluster, r.GetResourceType())
	}

	if r.Spec.ShardCount == 0 {
		return xerrors.New("ShardCount must be greater than 0 for sharded clusters")
	}

	authModes := r.Spec.GetSecurityAuthenticationModes()
	foundScram := false
	for _, authMode := range authModes {
		if strings.HasPrefix(strings.ToUpper(authMode), util.SCRAM) {
			foundScram = true
			break
		}
	}

	if !foundScram && len(authModes) > 0 {
		return xerrors.New("MongoDBSearch requires SCRAM authentication to be enabled")
	}

	// Validate that all shards have LB endpoints configured
	if r.search != nil && r.search.IsShardedExternalLB() {
		shardNames := r.GetShardNames()
		if err := r.search.ValidateShardEndpointsForCluster(shardNames); err != nil {
			return err
		}
	}

	return nil
}

// GetExternalLBEndpointForShard returns the external LB endpoint for a specific shard.
// Returns empty string if not configured.
func (r *ShardedEnterpriseSearchSource) GetExternalLBEndpointForShard(shardName string) string {
	if r.search == nil || !r.search.IsShardedExternalLB() {
		return ""
	}
	endpointMap := r.search.GetShardEndpointMap()
	return endpointMap[shardName]
}

// GetMongoDB returns the underlying MongoDB resource.
func (r *ShardedEnterpriseSearchSource) GetMongoDB() *mdbv1.MongoDB {
	return r.MongoDB
}

// GetSearch returns the MongoDBSearch resource.
func (r *ShardedEnterpriseSearchSource) GetSearch() *searchv1.MongoDBSearch {
	return r.search
}

// IsShardedSource returns true indicating this is a sharded search source.
func (r *ShardedEnterpriseSearchSource) IsShardedSource() bool {
	return true
}
