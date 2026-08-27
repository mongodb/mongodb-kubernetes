package multicluster

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	intp "github.com/mongodb/mongodb-kubernetes/pkg/util/int"
)

// shouldPerformFailover checks if the operator is configured to perform automatic failover
// of the MongoDB Replicaset members spread over multiple Kubernetes clusters.
func ShouldPerformFailover() bool {
	str := os.Getenv("PERFORM_FAILOVER") // nolint:forbidigo
	val, err := strconv.ParseBool(str)
	if err != nil {
		return false
	}
	return val
}

// MustGetClusterNumFromMultiStsName parses the statefulset object name and returns the cluster number where it is created
func MustGetClusterNumFromMultiStsName(name string) int {
	ss := strings.Split(name, "-")

	n, err := strconv.Atoi(ss[len(ss)-1])
	if err != nil {
		panic(err)
	}
	return n
}

// GetRsNamefromMultiStsName parses the statefulset object name and returns the name of MongoDBMultiCluster object name
func GetRsNamefromMultiStsName(name string) string {
	ss := strings.Split(name, "-")
	if len(ss) <= 1 || ss[0] == "" {
		panic(fmt.Sprintf("invalid statefulset name: %s", name))
	}
	return strings.Join(ss[:len(ss)-1], "-")
}

// MemberCluster is a wrapper type containing basic information about member cluster in one place.
// It is used to simplify reconciliation process and to ensure deterministic iteration over member clusters.
type MemberCluster struct {
	Name         string
	Index        int
	Replicas     int
	Client       kubernetesClient.Client
	SecretClient secrets.SecretClient
	// ResourceName is the MemberCluster CR's metadata.name, used to derive member-scoped
	// resource names. Empty for the legacy central cluster (single-cluster mode).
	ResourceName string
	// Active marks a cluster as a member holding database nodes. The flag is useful for only relying on active clusters when reading
	// information about the topology of the multi-cluster MongoDB or AppDB resource. This could mean automation config or cluster specific configuration.
	Active bool
	// Healthy marks if we have connection to the cluster.
	Healthy bool
	// Legacy if set to true, marks this cluster to use the old naming conventions (without the cluster index)
	Legacy bool
}

// LegacyCentralClusterName is a cluster name for simulating multi-cluster mode when running in legacy single-cluster mode
// With the deployment state in config maps and multi-cluster-first we might store this dummy cluster name in the state config map.
// We cannot change this name from now on.
const LegacyCentralClusterName = "__default"

// GetLegacyCentralMemberCluster returns a legacy central member cluster for unit tests.
// Such member cluster is created in the reconcile loop in SingleCluster topology
// in order to simulate multi-cluster deployment on one member cluster that has legacy naming conventions enabled.
func GetLegacyCentralMemberCluster(replicas int, index int, client kubernetesClient.Client, secretClient secrets.SecretClient) MemberCluster {
	return MemberCluster{
		Name:         LegacyCentralClusterName,
		Index:        index,
		Replicas:     replicas,
		Client:       client,
		SecretClient: secretClient,
		Active:       true,
		Healthy:      true,
		Legacy:       true,
	}
}

// CreateMapWithUpdatedMemberClusterIndexes returns a new mapping for memberClusterNames.
// It maintains previously existing mappings and assigns new indexes for new cluster names.
func AssignIndexesForMemberClusterNames(existingMapping map[string]int, memberClusterNames []string) map[string]int {
	newMapping := map[string]int{}
	for k, v := range existingMapping {
		newMapping[k] = v
	}

	for _, clusterName := range memberClusterNames {
		if _, ok := newMapping[clusterName]; !ok {
			newMapping[clusterName] = getNextIndex(newMapping)
		}
	}

	return newMapping
}

func getNextIndex(m map[string]int) int {
	maxi := -1

	for _, val := range m {
		maxi = intp.Max(maxi, val)
	}
	return maxi + 1
}

var memberClusterMapMutex sync.Mutex

// IsMemberClusterMapInitializedForMultiCluster checks if global member cluster map
// is properly initialized for multi-cluster workloads. The assumption is that if the map
// contains only __default cluster, that means it's not configured for multi-cluster.
func IsMemberClusterMapInitializedForMultiCluster(memberClusterMap map[string]Entry) bool {
	memberClusterMapMutex.Lock()
	defer memberClusterMapMutex.Unlock()

	if len(memberClusterMap) == 0 {
		return false
	} else if len(memberClusterMap) == 1 {
		if _, ok := memberClusterMap[LegacyCentralClusterName]; ok {
			return false
		}
	}

	return true
}

func InitializeGlobalMemberClusterMapForSingleCluster(globalMemberClustersMap map[string]Entry, defaultKubeClient client.Client) map[string]Entry {
	memberClusterMapMutex.Lock()
	defer memberClusterMapMutex.Unlock()

	if _, ok := globalMemberClustersMap[LegacyCentralClusterName]; !ok {
		if globalMemberClustersMap == nil {
			globalMemberClustersMap = map[string]Entry{}
		}
		globalMemberClustersMap[LegacyCentralClusterName] = Entry{Client: defaultKubeClient}
	}

	return globalMemberClustersMap
}
