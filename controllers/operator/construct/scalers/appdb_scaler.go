package scalers

import (
	"github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
)

func GetAppDBScaler(opsManager *om.MongoDBOpsManager, memberClusterName string, memberClusterNum int, prevMembers []multicluster.MemberCluster) interfaces.AppDBScaler {
	if opsManager.Spec.AppDB.IsMultiCluster() {
		return NewAppDBMultiClusterScaler(opsManager, memberClusterName, memberClusterNum, prevMembers)
	} else {
		return NewAppDBSingleClusterScaler(opsManager)
	}
}

type AppDBMultiClusterScaler struct {
	opsManager        *om.MongoDBOpsManager
	memberClusterName string
	memberClusterNum  int
	prevMembers       []multicluster.MemberCluster
}

func NewAppDBMultiClusterScaler(opsManager *om.MongoDBOpsManager, memberClusterName string, memberClusterNum int, prevMembers []multicluster.MemberCluster) *AppDBMultiClusterScaler {
	return &AppDBMultiClusterScaler{
		opsManager:        opsManager,
		memberClusterName: memberClusterName,
		memberClusterNum:  memberClusterNum,
		prevMembers:       prevMembers,
	}
}

func (s *AppDBMultiClusterScaler) ForcedIndividualScaling() bool {
	// When scaling AppDB up the first time, it's safe to add all the members.
	// When adding a new cluster, we want to force individual scaling because ReplicasThisReconciliation
	// short circuits the one-by-one scaling when individual scaling is disabled and starting replicas is zero.
	if s.ScalingFirstTime() {
		return false
	} else {
		return true
	}
}

func (s *AppDBMultiClusterScaler) DesiredReplicas() int {
	if s.ScalingFirstTime() {
		return s.opsManager.Spec.AppDB.GetMemberClusterSpecByName(s.memberClusterName).Members
	}
	previousMembers := 0
	for _, memberCluster := range s.prevMembers {
		if memberCluster.Name == s.memberClusterName {
			previousMembers = memberCluster.Replicas
			break
		}
	}

	// Example:
	// spec:
	// cluster-1: 3
	// cluster-2: 5
	// cluster-3: 1
	//
	// previous:
	// cluster-1: 3
	// cluster-2: 2
	// cluster-3: 0
	//
	// scaler cluster-1:
	// current: 3
	// desired: 3 -> return previousMembers
	// replicasThisReconcile: 3
	//
	// scaler cluster-2:
	// current: 2
	// desired: 5 -> return replicasInSpec
	// replicasThisReconcile: 3
	//
	// scaler cluster-3:
	// current: 0
	// desired: 0 -> return previousMembers
	// replicasThisReconcile: 0
	for _, memberCluster := range s.prevMembers {
		replicasInSpec := s.opsManager.Spec.AppDB.GetMemberClusterSpecByName(memberCluster.Name).Members
		// find the first cluster with a different desired spec
		if replicasInSpec != memberCluster.Replicas {
			// if it's a different cluster, we don't scale this cluster up or down
			if memberCluster.Name != s.memberClusterName {
				return previousMembers
			} else {
				return replicasInSpec
			}
		}
	}
	return previousMembers
}

func (s *AppDBMultiClusterScaler) CurrentReplicas() int {
	for _, memberCluster := range s.prevMembers {
		if memberCluster.Name == s.memberClusterName {
			return memberCluster.Replicas
		}
	}
	return 0
}

func (s *AppDBMultiClusterScaler) ScalingFirstTime() bool {
	for _, memberCluster := range s.prevMembers {
		if memberCluster.Replicas != 0 {
			return false
		}
	}
	return true
}

func (s *AppDBMultiClusterScaler) MemberClusterName() string {
	return s.memberClusterName
}

func (s *AppDBMultiClusterScaler) MemberClusterNum() int {
	return s.memberClusterNum
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
	return om.DummmyCentralClusterName
}

func (s *AppDBSingleClusterScaler) MemberClusterNum() int {
	return 0
}
