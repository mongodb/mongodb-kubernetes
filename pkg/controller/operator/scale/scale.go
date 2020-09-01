package scale

import "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"

// ReplicaSetScaler is an interface which is able to scale up and down a replicaset
// a single member at a time
type ReplicaSetScaler interface {
	DesiredReplicaSetMembers() int
	CurrentReplicaSetMembers() int
}

// ReplicasThisReconciliation returns the number of replicas that should be configured
// for that reconciliation. As of MongoDB 4.4 we can only scale members up / down 1 at a time.
func ReplicasThisReconciliation(replicaSetScaler ReplicaSetScaler) int {
	// the current replica set members will be 0 when we are creating a new deployment
	// if this is the case, we want to jump straight to the desired members and not make changes incrementally
	if replicaSetScaler.CurrentReplicaSetMembers() == 0 || replicaSetScaler.CurrentReplicaSetMembers() == replicaSetScaler.DesiredReplicaSetMembers() {
		return replicaSetScaler.DesiredReplicaSetMembers()
	}

	if isScalingDown(replicaSetScaler) {
		return replicaSetScaler.CurrentReplicaSetMembers() - 1
	}

	return replicaSetScaler.CurrentReplicaSetMembers() + 1
}

func IsStillScaling(replicaSetScaler ReplicaSetScaler) bool {
	return ReplicasThisReconciliation(replicaSetScaler) != replicaSetScaler.DesiredReplicaSetMembers()
}

func isScalingDown(replicaSetScaler ReplicaSetScaler) bool {
	return replicaSetScaler.DesiredReplicaSetMembers() < replicaSetScaler.CurrentReplicaSetMembers()
}

// AnyAreStillScaling reports true if any of one the provided members is still scaling
func AnyAreStillScaling(scalers ...ReplicaSetScaler) bool {
	for _, s := range scalers {
		if IsStillScaling(s) {
			return true
		}
	}
	return false
}

func MembersOption(replicaSetScaler ReplicaSetScaler) status.Option {
	return ReplicaSetMembersOption{Members: ReplicasThisReconciliation(replicaSetScaler)}
}

func MongosCountOption(replicaSetScaler ReplicaSetScaler) status.Option {
	return ShardedClusterMongosOption{Members: ReplicasThisReconciliation(replicaSetScaler)}
}
func ConfigServerOption(replicaSetScaler ReplicaSetScaler) status.Option {
	return ShardedClusterConfigServerOption{Members: ReplicasThisReconciliation(replicaSetScaler)}
}

func MongodsPerShardOption(replicaSetScaler ReplicaSetScaler) status.Option {
	return ShardedClusterMongodsPerShardCountOption{Members: ReplicasThisReconciliation(replicaSetScaler)}
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
