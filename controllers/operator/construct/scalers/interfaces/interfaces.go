package interfaces

import "github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"

type AppDBScaler interface {
	scale.ReplicaSetScaler
	MemberClusterName() string
	MemberClusterNum() int
}
