// Package resourcenames centralises the names of resources used for MCK multi-cluster
// configuration. These names form a contract shared by the kubectl plugin and the operator.
//
// The <member-cluster> segment used by the functions in this package is the RFC 1123 name
// (MemberCluster CR metadata.name), not the logical spec.clusterName.
package resourcenames

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	// memberClusterResourceNamePrefix prefixes every member-cluster RBAC resource name.
	memberClusterResourceNamePrefix = "mck-member-"
)

// MemberClusterResourceName returns the base name (mck-member-<member-cluster>) shared by the
// member-cluster RBAC resources on the member cluster; individual resources append a suffix
// (-sa, -token, -role, -role-binding).
func MemberClusterResourceName(memberClusterName string) string {
	return memberClusterResourceNamePrefix + memberClusterName
}

// MemberClusterTokenSecretName returns the name of the long-lived ServiceAccount token Secret on the
// member cluster for that cluster's operator ServiceAccount (mck-member-<member-cluster>-sa).
func MemberClusterTokenSecretName(memberClusterName string) string {
	return MemberClusterResourceName(memberClusterName) + "-token"
}

// workloadServiceAccount pairs a member-scoped suffix with the fixed helm-install SA name.
type workloadServiceAccount struct {
	suffix    string
	fixedName string
}

var (
	WorkloadAppDBServiceAccount        = workloadServiceAccount{suffix: "appdb", fixedName: util.AppDBServiceAccount}
	WorkloadDatabasePodsServiceAccount = workloadServiceAccount{suffix: "database-pods", fixedName: util.MongoDBServiceAccount}
	WorkloadOpsManagerServiceAccount   = workloadServiceAccount{suffix: "ops-manager", fixedName: util.OpsManagerServiceAccount}
)

// Name returns the ServiceAccount name for this workload on the given cluster. resourceName is
// the MemberCluster CR's metadata.name (multicluster.Entry.ResourceName). baseInstall
// selects the fixed SA name provided by the base installation (helm/OLM); otherwise the
// member-scoped mck-member-<metadata.name>-<suffix> name is used.
func (w workloadServiceAccount) Name(resourceName string, baseInstall bool) string {
	if baseInstall {
		return w.fixedName
	}
	return MemberClusterResourceName(resourceName) + "-" + w.suffix
}
