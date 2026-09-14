// Package resourcenames centralises the names of resources used for MCK multi-cluster
// configuration. These names form a contract shared by the kubectl plugin and the operator.
//
// Member-side RBAC names (mck-member-*) are fixed: one render is applied to every member
// cluster. Central-side per-cluster names (MemberCluster CR metadata.name and the credential
// Secret derived from it via MemberClusterCredentialSecretName) use the RFC 1123 member
// cluster name, not the logical spec.clusterName.
//
// The member ServiceAccount is rendered by the plugin under the default name
// MemberClusterServiceAccountName unless the user pre-provisions their own and passes it
// via --member-cluster-service-account; the token Secret is discovered via the
// kubernetes.io/service-account.name annotation, never looked up by name.
package resourcenames

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	// memberClusterResourceName is the fixed base name (mck-member) shared by the
	// member-cluster RBAC resources on every member cluster; individual resources append a
	// suffix (-sa, -token, -appdb, -database-pods, -ops-manager, ...).
	memberClusterResourceName = "mck-member"

	// MemberClusterServiceAccountName is the default name of the member ServiceAccount the
	// plugin renders (and registration discovers the token for) when the user does not
	// pre-provision their own via --member-cluster-service-account.
	MemberClusterServiceAccountName = memberClusterResourceName + "-sa"
)

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
