package searchcontroller

import (
	"fmt"
	"strings"

	"github.com/blang/semver"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// ShardedInternalSearchSource implements SearchSourceDBResource for sharded MongoDB clusters.
// It provides per-shard host seeds and unmanaged LB endpoint mapping.
type ShardedInternalSearchSource struct {
	*mdbv1.MongoDB
	search *searchv1.MongoDBSearch
}

func NewShardedInternalSearchSource(mdb *mdbv1.MongoDB, search *searchv1.MongoDBSearch) *ShardedInternalSearchSource {
	return &ShardedInternalSearchSource{
		MongoDB: mdb,
		search:  search,
	}
}

// GetShardNames returns the names of the shards search must serve, including any
// shard still draining on the source (see GetShardCount for the drain rationale).
func (r *ShardedInternalSearchSource) GetShardNames() []string {
	count := r.GetShardCount()
	names := make([]string, count)
	for i := 0; i < count; i++ {
		names[i] = r.ShardRsName(i)
	}
	return names
}

// GetShardCount returns max(spec, achieved) shard count. The sharded-cluster
// reconciler stamps Status.ShardCount only once the cluster reaches Running,
// i.e. after drain phase 2, so it lags Spec.ShardCount during a shard-count
// reduction; using the max keeps search serving the draining shards meanwhile.
func (r *ShardedInternalSearchSource) GetShardCount() int {
	return max(r.Spec.ShardCount, r.Status.ShardCount)
}

// DrainingShardCount is the number of shards still draining on the source —
// the gap between the count search serves (GetShardCount) and the desired spec.
func (r *ShardedInternalSearchSource) DrainingShardCount() int {
	return r.GetShardCount() - r.Spec.ShardCount
}

func (r *ShardedInternalSearchSource) HostSeeds(shardName string) ([]string, error) {
	members := r.Spec.MongodsPerShardCount
	clusterDomain := r.Spec.GetClusterDomain()
	port := r.Spec.GetAdditionalMongodConfig().GetPortOrDefault()

	seeds := make([]string, members)
	for i := 0; i < members; i++ {
		// Format: <shardName>-<memberIdx>.<shardServiceName>.<namespace>.svc.<clusterDomain>:<port>
		seeds[i] = fmt.Sprintf("%s-%d.%s.%s.svc.%s:%d",
			shardName, i, r.ShardServiceName(), r.Namespace, clusterDomain, port)
	}
	return seeds, nil
}

func (r *ShardedInternalSearchSource) MongosHostsAndPorts() []string {
	clusterDomain := r.Spec.GetClusterDomain()
	port := r.Spec.GetAdditionalMongodConfig().GetPortOrDefault()
	return []string{fmt.Sprintf("%s.%s.svc.%s:%d", r.ServiceName(), r.Namespace, clusterDomain, port)}
}

func (r *ShardedInternalSearchSource) TLSConfig() *TLSSourceConfig {
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

func (r *ShardedInternalSearchSource) KeyfileSecretName() string {
	return fmt.Sprintf("%s-%s", r.Name, MongotKeyfileFilename)
}

func (r *ShardedInternalSearchSource) ResourceType() mdbv1.ResourceType {
	return r.GetResourceType()
}

func (r *ShardedInternalSearchSource) Validate() error {
	version, err := semver.ParseTolerant(util.StripEnt(r.Spec.GetMongoDBVersion()))
	if err != nil {
		return xerrors.Errorf("error parsing MongoDB version '%s': %w", r.Spec.GetMongoDBVersion(), err)
	} else if version.LT(semver.MustParse("8.2.0")) {
		return xerrors.New("MongoDB version must be 8.2.0 or higher")
	}

	if r.GetResourceType() != mdbv1.ShardedCluster {
		return xerrors.Errorf("ShardedInternalSearchSource requires a %s resource, got %s", mdbv1.ShardedCluster, r.GetResourceType())
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

	return nil
}

func (r *ShardedInternalSearchSource) GetUnmanagedLBEndpointForShard(shardName string) string {
	if r.search == nil || !r.search.IsShardedUnmanagedLB() {
		return ""
	}
	return r.search.GetEndpointForShard(shardName)
}
