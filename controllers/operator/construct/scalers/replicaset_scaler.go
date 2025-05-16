package scalers

import (
	"fmt"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/scale"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// MultiClusterReplicaSetScaler is a generic scaler that can be user in any multi-cluster replica set.
type MultiClusterReplicaSetScaler struct {
	clusterSpecList   mdbv1.ClusterSpecList
	memberClusterName string
	memberClusterNum  int
	prevMembers       []multicluster.MemberCluster
	scalerDescription string
}

func NewMultiClusterReplicaSetScaler(scalerDescription string, clusterSpecList mdbv1.ClusterSpecList, memberClusterName string, memberClusterNum int, prevMembers []multicluster.MemberCluster) *MultiClusterReplicaSetScaler {
	return &MultiClusterReplicaSetScaler{
		scalerDescription: scalerDescription,
		clusterSpecList:   clusterSpecList,
		memberClusterName: memberClusterName,
		memberClusterNum:  memberClusterNum,
		prevMembers:       prevMembers,
	}
}

func getMemberClusterItemByClusterName(clusterSpecList mdbv1.ClusterSpecList, memberClusterName string) mdbv1.ClusterSpecItem {
	for _, clusterSpec := range clusterSpecList {
		if clusterSpec.ClusterName == memberClusterName {
			return clusterSpec
		}
	}

	// In case the member cluster is not found in the cluster spec list, we return an empty ClusterSpecItem
	// with 0 members to handle the case of removing a cluster from the spec list without a panic.
	return mdbv1.ClusterSpecItem{
		ClusterName: memberClusterName,
		Members:     0,
	}
}

func (s *MultiClusterReplicaSetScaler) ForcedIndividualScaling() bool {
	// When scaling ReplicaSet for the first time, it's safe to add all the members.
	// When adding a new cluster, we want to force individual scaling because ReplicasThisReconciliation
	// short circuits the one-by-one scaling when individual scaling is disabled and starting replicas is zero.
	if s.ScalingFirstTime() {
		return false
	} else {
		return true
	}
}

// DesiredReplicas returns desired replicas for the statefulset in one member cluster.
// Important: if other scalers (for other statefulsets) are still scaling, then this scaler will return
// the previous member count instead of the true desired member count to guarantee that we change only
// one member of the replica set across all scalers (for statefulsets in different member clusters).
func (s *MultiClusterReplicaSetScaler) DesiredReplicas() int {
	if s.ScalingFirstTime() {
		return getMemberClusterItemByClusterName(s.clusterSpecList, s.memberClusterName).Members
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
		replicasInSpec := getMemberClusterItemByClusterName(s.clusterSpecList, memberCluster.Name).Members
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

// TargetReplicas always returns the true replicas that the statefulset should have in this cluster regardless
// whether other scalers are still scaling or not.
func (s *MultiClusterReplicaSetScaler) TargetReplicas() int {
	return getMemberClusterItemByClusterName(s.clusterSpecList, s.memberClusterName).Members
}

func (s *MultiClusterReplicaSetScaler) CurrentReplicas() int {
	for _, memberCluster := range s.prevMembers {
		if memberCluster.Name == s.memberClusterName {
			return memberCluster.Replicas
		}
	}
	return 0
}

func (s *MultiClusterReplicaSetScaler) ScalingFirstTime() bool {
	for _, memberCluster := range s.prevMembers {
		if memberCluster.Replicas != 0 {
			return false
		}
	}
	return true
}

func (s *MultiClusterReplicaSetScaler) MemberClusterName() string {
	return s.memberClusterName
}

func (s *MultiClusterReplicaSetScaler) MemberClusterNum() int {
	return s.memberClusterNum
}

func (s *MultiClusterReplicaSetScaler) ScalerDescription() string {
	return s.scalerDescription
}

func (s *MultiClusterReplicaSetScaler) String() string {
	return fmt.Sprintf("{MultiClusterReplicaSetScaler (%s): still scaling: %t (finishing this reconcile: %t), clusterName=%s, clusterIdx=%d, current/target replicas:%d/%d, "+
		"replicas this reconciliation: %d, scaling first time: %t}", s.scalerDescription, s.CurrentReplicas() != s.TargetReplicas(), scale.ReplicasThisReconciliation(s) == s.TargetReplicas(), s.memberClusterName, s.memberClusterNum,
		s.CurrentReplicas(), s.TargetReplicas(), scale.ReplicasThisReconciliation(s), s.ScalingFirstTime())
}
