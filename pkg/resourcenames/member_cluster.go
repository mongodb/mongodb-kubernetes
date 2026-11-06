// Package resourcenames centralises the names of resources used for MCK multi-cluster
// configuration. These names form a contract shared by the kubectl plugin and the operator.
//
// Member-side RBAC names (mck-member-*) are fixed: one render is applied to every member
// cluster. Central-side per-cluster names (MemberCluster CR metadata.name and the credential
// Secret derived from it via MemberClusterCredentialSecretName) use the RFC 1123 member
// cluster name, not the logical spec.clusterName.
package resourcenames

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	// memberClusterResourceName is the fixed base name (mck-member) shared by the
	// member-cluster RBAC resources on every member cluster; individual resources append a
	// suffix (-sa, -token, -role-base, -role-multicluster, ...).
	memberClusterResourceName = "mck-member"
)

// MemberClusterTokenSecretName returns the name of the long-lived ServiceAccount token Secret on the
// member cluster for the operator's member ServiceAccount (mck-member-sa).
func MemberClusterTokenSecretName() string {
	return memberClusterResourceName + "-token"
}

// workloadServiceAccount pairs the fixed member-side SA name with the fixed helm-install
// SA name.
type workloadServiceAccount struct {
	memberName      string
	baseInstallName string
}

var (
	WorkloadAppDBServiceAccount        = workloadServiceAccount{memberName: memberClusterResourceName + "-appdb", baseInstallName: util.AppDBServiceAccount}
	WorkloadDatabasePodsServiceAccount = workloadServiceAccount{memberName: memberClusterResourceName + "-database-pods", baseInstallName: util.MongoDBServiceAccount}
	WorkloadOpsManagerServiceAccount   = workloadServiceAccount{memberName: memberClusterResourceName + "-ops-manager", baseInstallName: util.OpsManagerServiceAccount}
)

// Name returns the ServiceAccount name for this workload. baseInstall selects the fixed SA
// name provided by the base installation (helm/OLM); otherwise the fixed member-side
// mck-member-* name is used (identical on every member cluster).
func (w workloadServiceAccount) Name(baseInstall bool) string {
	if baseInstall {
		return w.baseInstallName
	}
	return w.memberName
}
