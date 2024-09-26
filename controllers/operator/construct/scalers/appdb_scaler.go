package scalers

import (
	"github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
)

func GetAppDBScaler(opsManager *om.MongoDBOpsManager, memberClusterName string, memberClusterNum int, prevMembers []multicluster.MemberCluster) interfaces.AppDBScaler {
	if opsManager.Spec.AppDB.IsMultiCluster() {
		return NewMultiClusterReplicaSetScaler(opsManager.Spec.AppDB.ClusterSpecList, memberClusterName, memberClusterNum, prevMembers)
	} else {
		return NewAppDBSingleClusterScaler(opsManager)
	}
}

// this is the implementation that originally was in om.MongoDBOpsManager
type AppDBSingleClusterScaler struct {
	opsManager *om.MongoDBOpsManager
}

func NewAppDBSingleClusterScaler(opsManager *om.MongoDBOpsManager) *AppDBSingleClusterScaler {
	return &AppDBSingleClusterScaler{
		opsManager: opsManager,
	}
}

func (s *AppDBSingleClusterScaler) ForcedIndividualScaling() bool {
	return false
}

func (s *AppDBSingleClusterScaler) DesiredReplicas() int {
	return s.opsManager.Spec.AppDB.Members
}

func (s *AppDBSingleClusterScaler) CurrentReplicas() int {
	return s.opsManager.Status.AppDbStatus.Members
}

func (s *AppDBSingleClusterScaler) MemberClusterName() string {
	return multicluster.LegacyCentralClusterName
}

func (s *AppDBSingleClusterScaler) MemberClusterNum() int {
	return 0
}
