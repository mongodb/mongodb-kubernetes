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
	externalMembers := r.externalMembersForK8sShardName(shardName)
	return append(r.kubernetesMongodHosts(shardName), externalHostnames(externalMembers)...), nil
}

// kubernetesMongodHosts returns the pod FQDNs of the Kubernetes mongods in one shard, in the
// form <shardName>-<memberIdx>.<shardServiceName>.<namespace>.svc.<clusterDomain>:<port>.
func (r *ShardedInternalSearchSource) kubernetesMongodHosts(shardName string) []string {
	clusterDomain := r.Spec.GetClusterDomain()
	port := r.Spec.GetAdditionalMongodConfig().GetPortOrDefault()

	hosts := make([]string, 0, r.Spec.MongodsPerShardCount)
	for i := 0; i < r.Spec.MongodsPerShardCount; i++ {
		hosts = append(hosts, fmt.Sprintf("%s-%d.%s.%s.svc.%s:%d",
			shardName, i, r.ShardServiceName(), r.Namespace, clusterDomain, port))
	}
	return hosts
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

// MongosHostsAndPorts returns the routers mongot should talk to. The Kubernetes mongos
// Service comes first, then any external (VM) mongos still in the cluster.
func (r *ShardedInternalSearchSource) MongosHostsAndPorts() []string {
	return append(r.kubernetesMongosHosts(), externalMongosHostnames(r.Spec.ExternalMembers)...)
}

// kubernetesMongosHosts returns the Kubernetes mongos Service, or an empty slice while
// mongosCount is 0, in which case the Service resolves to no endpoints.
func (r *ShardedInternalSearchSource) kubernetesMongosHosts() []string {
	hosts := make([]string, 0, 1)
	if r.Spec.MongosCount == 0 {
		return hosts
	}
	clusterDomain := r.Spec.GetClusterDomain()
	port := r.Spec.GetAdditionalMongodConfig().GetPortOrDefault()
	return append(hosts, fmt.Sprintf("%s.%s.svc.%s:%d", r.ServiceName(), r.Namespace, clusterDomain, port))
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
