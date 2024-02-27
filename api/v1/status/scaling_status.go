package status

import (
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"
)

func MembersOption(replicaSetscaler scale.ReplicaSetScaler) Option {
	return ReplicaSetMembersOption{Members: scale.ReplicasThisReconciliation(replicaSetscaler)}
}

func MongosCountOption(replicaSetscaler scale.ReplicaSetScaler) Option {
	return ShardedClusterMongosOption{Members: scale.ReplicasThisReconciliation(replicaSetscaler)}
}
func ConfigServerOption(replicaSetscaler scale.ReplicaSetScaler) Option {
	return ShardedClusterConfigServerOption{Members: scale.ReplicasThisReconciliation(replicaSetscaler)}
}

func MongodsPerShardOption(replicaSetscaler scale.ReplicaSetScaler) Option {
	return ShardedClusterMongodsPerShardCountOption{Members: scale.ReplicasThisReconciliation(replicaSetscaler)}
}

func AppDBMemberOptions(appDBScalers ...interfaces.AppDBScaler) Option {
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
