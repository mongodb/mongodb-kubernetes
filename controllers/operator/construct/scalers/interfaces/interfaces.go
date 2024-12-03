package interfaces

import "github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"

type MultiClusterReplicaSetScaler interface {
	scale.ReplicaSetScaler
	ScalingFirstTime() bool
	MemberClusterName() string
	MemberClusterNum() int
}
