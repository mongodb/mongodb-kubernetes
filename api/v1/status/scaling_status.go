package status

import (
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

// ReplicaSetMembersOption is required in order to ensure that the status of a resource
// is only updated one member at a time. The logic which scales incrementally relies
// on the current status of the resource to accurate
type ReplicaSetMembersOption struct {
	Members int
}

func (o ReplicaSetMembersOption) Value() interface{} {
	return o.Members
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
