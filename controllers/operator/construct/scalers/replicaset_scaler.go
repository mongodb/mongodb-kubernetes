package scalers

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
)

// MultiClusterReplicaSetScaler is a generic scaler that can be user in any multi-cluster replica set.
type MultiClusterReplicaSetScaler struct {
	clusterSpecList   mdbv1.ClusterSpecList
	memberClusterName string
	memberClusterNum  int
	prevMembers       []multicluster.MemberCluster
}

func NewMultiClusterReplicaSetScaler(clusterSpecList mdbv1.ClusterSpecList, memberClusterName string, memberClusterNum int, prevMembers []multicluster.MemberCluster) *MultiClusterReplicaSetScaler {
	return &MultiClusterReplicaSetScaler{
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
