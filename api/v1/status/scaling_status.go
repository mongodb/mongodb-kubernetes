package status

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/scale"
)

func MembersOption(replicaSetscaler scale.ReplicaSetScaler) Option {
	return ReplicaSetMembersOption{Members: scale.ReplicasThisReconciliation(replicaSetscaler)}
}

func AppDBMemberOptions(appDBScalers ...interfaces.MultiClusterReplicaSetScaler) Option {
	members := 0
	clusterStatusList := []ClusterStatusItem{}
	for _, scaler := range appDBScalers {
		members += scale.ReplicasThisReconciliation(scaler)
		clusterStatusList = append(clusterStatusList, ClusterStatusItem{
			Members:     scale.ReplicasThisReconciliation(scaler),
			ClusterName: scaler.MemberClusterName(),
		})
	}
	return MultiReplicaSetMemberOption{Members: members, ClusterStatusList: clusterStatusList}
}

// ReplicaSetMembersOption is required in order to ensure that the status of a resource
// is only updated one member at a time. The logic which scales incrementally relies
// on the current status of the resource to be accurate.
type ReplicaSetMembersOption struct {
	Members int
}

func (o ReplicaSetMembersOption) Value() interface{} {
	return o.Members
}

type OMClusterStatusItem struct {
	ClusterName string `json:"clusterName,omitempty"`
	Replicas    int    `json:"replicas,omitempty"`
}

type ClusterStatusItem struct {
	ClusterName string `json:"clusterName,omitempty"`
	Members     int    `json:"members,omitempty"`
}

type ClusterStatusList struct {
	ClusterStatuses []ClusterStatusItem `json:"clusterStatuses,omitempty"`
}

type MultiReplicaSetMemberOption struct {
	Members           int
	ClusterStatusList []ClusterStatusItem
}

func (o MultiReplicaSetMemberOption) Value() interface{} {
	return struct {
		Members           int
		ClusterStatusList []ClusterStatusItem
	}{
		o.Members,
		o.ClusterStatusList,
	}
}

type ShardedClusterMongodsPerShardCountOption struct {
	Members int
}

func (o ShardedClusterMongodsPerShardCountOption) Value() interface{} {
	return o.Members
}

type ShardedClusterConfigServerOption struct {
	Members int
}

func (o ShardedClusterConfigServerOption) Value() interface{} {
	return o.Members
}

type ShardedClusterMongosOption struct {
	Members int
}

func (o ShardedClusterMongosOption) Value() interface{} {
	return o.Members
}

type ShardedClusterSizeConfigOption struct {
	SizeConfig *MongodbShardedClusterSizeConfig
}

func (o ShardedClusterSizeConfigOption) Value() interface{} {
	return o.SizeConfig
}

type ShardedClusterSizeStatusInClustersOption struct {
	SizeConfigInClusters *MongodbShardedSizeStatusInClusters
}

func (o ShardedClusterSizeStatusInClustersOption) Value() interface{} {
	return o.SizeConfigInClusters
}

// MongodbShardedClusterSizeConfig describes the numbers and sizes of replica sets inside sharded cluster
// +k8s:deepcopy-gen=true
type MongodbShardedClusterSizeConfig struct {
	ShardCount           int `json:"shardCount,omitempty"`
	MongodsPerShardCount int `json:"mongodsPerShardCount,omitempty"`
	MongosCount          int `json:"mongosCount,omitempty"`
	ConfigServerCount    int `json:"configServerCount,omitempty"`
}

func (m *MongodbShardedClusterSizeConfig) String() string {
	return fmt.Sprintf("%+v", *m)
}

// MongodbShardedSizeStatusInClusters describes the number and sizes of replica sets members deployed across member clusters
// +k8s:deepcopy-gen=true
type MongodbShardedSizeStatusInClusters struct {
	ShardMongodsInClusters        map[string]int            `json:"shardMongodsInClusters,omitempty"`
	ShardOverridesInClusters      map[string]map[string]int `json:"shardOverridesInClusters,omitempty"`
	MongosCountInClusters         map[string]int            `json:"mongosCountInClusters,omitempty"`
	ConfigServerMongodsInClusters map[string]int            `json:"configServerMongodsInClusters,omitempty"`
}

func String(m *MongodbShardedSizeStatusInClusters) string {
	return fmt.Sprintf("%+v", *m)
}

func sumMap(m map[string]int) int {
	sum := 0
	for _, v := range m {
		sum += v
	}
	return sum
}

func (s *MongodbShardedSizeStatusInClusters) TotalShardMongodsInClusters() int {
	return sumMap(s.ShardMongodsInClusters)
}

func (s *MongodbShardedSizeStatusInClusters) TotalConfigServerMongodsInClusters() int {
	return sumMap(s.ConfigServerMongodsInClusters)
}

func (s *MongodbShardedSizeStatusInClusters) TotalMongosCountInClusters() int {
	return sumMap(s.MongosCountInClusters)
}
