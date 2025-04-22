package interfaces

import "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/scale"

type MultiClusterReplicaSetScaler interface {
	scale.ReplicaSetScaler
	ScalingFirstTime() bool
	TargetReplicas() int
	MemberClusterName() string
	MemberClusterNum() int
	// ScalerDescription contains the name of the component associated to that scaler (shard, config server, AppDB...)
	ScalerDescription() string
}
