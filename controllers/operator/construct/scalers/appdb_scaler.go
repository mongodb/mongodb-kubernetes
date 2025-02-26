package scalers

import (
	"github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
)

func GetAppDBScaler(opsManager *om.MongoDBOpsManager, memberClusterName string, memberClusterNum int, prevMembers []multicluster.MemberCluster) interfaces.MultiClusterReplicaSetScaler {
	if opsManager.Spec.AppDB.IsMultiCluster() {
		return NewMultiClusterReplicaSetScaler("AppDB", opsManager.Spec.AppDB.ClusterSpecList, memberClusterName, memberClusterNum, prevMembers)
	} else {
		return NewAppDBSingleClusterScaler(opsManager)
	}
}

// this is the implementation that originally was in om.MongoDBOpsManager
type appDBSingleClusterScaler struct {
	opsManager *om.MongoDBOpsManager
}

func NewAppDBSingleClusterScaler(opsManager *om.MongoDBOpsManager) interfaces.MultiClusterReplicaSetScaler {
	return &appDBSingleClusterScaler{
		opsManager: opsManager,
	}
}

func (s *appDBSingleClusterScaler) ForcedIndividualScaling() bool {
	return false
}

func (s *appDBSingleClusterScaler) DesiredReplicas() int {
	return s.opsManager.Spec.AppDB.Members
}

func (s *appDBSingleClusterScaler) TargetReplicas() int {
	return s.DesiredReplicas()
}

func (s *appDBSingleClusterScaler) CurrentReplicas() int {
	return s.opsManager.Status.AppDbStatus.Members
}

func (s *appDBSingleClusterScaler) ScalingFirstTime() bool {
	return true
}

func (s *appDBSingleClusterScaler) MemberClusterName() string {
	return multicluster.LegacyCentralClusterName
}

func (s *appDBSingleClusterScaler) MemberClusterNum() int {
	return 0
}

func (s *appDBSingleClusterScaler) ScalerDescription() string { return "AppDB" }
