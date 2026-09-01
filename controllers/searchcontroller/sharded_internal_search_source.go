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

func (r *ShardedInternalSearchSource) GetShardNames() []string {
	return r.ShardNames()
}

func (r *ShardedInternalSearchSource) GetShardCount() int {
	return r.Spec.ShardCount
}

// externalMembersForK8sShardName maps the Kubernetes shard name the search plan works with
// (MongoDB.ShardName(i)) to the Automation Config replica set name that external members are
// recorded under (MongoDB.ShardACRsName(i), which spec.shardNameOverrides can change), and
// returns that shard's external mongod members.
func (r *ShardedInternalSearchSource) externalMembersForK8sShardName(shardName string) []mdbv1.ExternalMember {
	for i := 0; i < r.Spec.ShardCount; i++ {
		if r.ShardName(i) == shardName {
			return r.Spec.GetExternalMembersForRS(r.ShardACRsName(i))
		}
	}
	return nil
}

// HostSeeds returns the hosts mongot syncs one shard's data from. Kubernetes mongods come
// first, then any external (VM) members still in the shard.
func (r *ShardedInternalSearchSource) HostSeeds(shardName string) ([]string, error) {
	members := r.Spec.MongodsPerShardCount
	clusterDomain := r.Spec.GetClusterDomain()
	port := r.Spec.GetAdditionalMongodConfig().GetPortOrDefault()

	externalMembers := r.externalMembersForK8sShardName(shardName)
	seeds := make([]string, 0, members+len(externalMembers))
	for i := 0; i < members; i++ {
		// Format: <shardName>-<memberIdx>.<shardServiceName>.<namespace>.svc.<clusterDomain>:<port>
		seeds = append(seeds, fmt.Sprintf("%s-%d.%s.%s.svc.%s:%d",
			shardName, i, r.ShardServiceName(), r.Namespace, clusterDomain, port))
	}
	return append(seeds, externalHostnames(externalMembers)...), nil
}

// externalHostnames returns the hostnames of the given external members.
func externalHostnames(members []mdbv1.ExternalMember) []string {
	hostnames := make([]string, 0, len(members))
	for _, m := range members {
		hostnames = append(hostnames, m.Hostname)
	}
	return hostnames
}

// externalMongosHostnames returns the hostnames of the given external members that are mongos.
func externalMongosHostnames(members []mdbv1.ExternalMember) []string {
	hostnames := make([]string, 0, len(members))
	for _, m := range members {
		if m.Type == mdbv1.ExternalMemberTypeMongos {
			hostnames = append(hostnames, m.Hostname)
		}
	}
	return hostnames
}

// MongosHostsAndPorts returns the routers mongot should talk to: the Kubernetes mongos Service
// plus any external (VM) mongos still in the cluster. The Service entry is omitted while
// mongosCount is 0 (a valid mid-migration state) because it would resolve to no endpoints.
func (r *ShardedInternalSearchSource) MongosHostsAndPorts() []string {
	clusterDomain := r.Spec.GetClusterDomain()
	port := r.Spec.GetAdditionalMongodConfig().GetPortOrDefault()

	hosts := make([]string, 0, 1+len(r.Spec.ExternalMembers))
	if r.Spec.MongosCount > 0 {
		hosts = append(hosts, fmt.Sprintf("%s.%s.svc.%s:%d", r.ServiceName(), r.Namespace, clusterDomain, port))
	}
	return append(hosts, externalMongosHostnames(r.Spec.ExternalMembers)...)
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

	if err := validateSearchSourceExternalDomain(&r.Spec); err != nil {
		return err
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
