// Package resourcenames centralises the names of resources used for MCK multi-cluster
// configuration. These names form a contract shared by the kubectl plugin and the operator.
//
// The <member-cluster> segment used by the functions in this package is the RFC 1123 name
// (MemberCluster CR metadata.name), not the logical spec.clusterName.
package resourcenames

import (
	"sync"

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

// resourceNames maps spec.clusterName → MemberCluster CR metadata.name. It is set once at
// operator startup from Discover's result; the operator restarts on membership changes, so
// write-once-at-startup is safe.
// TODO(m1kola): slice-9: remove this registry when the MemberCluster watch becomes reactive (no restart); the resource-name mapping must then flow with the cluster objects themselves.
var (
	resourceNamesMu sync.RWMutex
	resourceNames   map[string]string
)

// SetResourceNames sets the spec.clusterName → CR metadata.name mapping. A nil map resets the
// registry (used by tests).
func SetResourceNames(names map[string]string) {
	resourceNamesMu.Lock()
	defer resourceNamesMu.Unlock()
	resourceNames = names
}

// ResourceNameForCluster returns the MemberCluster CR metadata.name for the given
// spec.clusterName. If the registry has no entry it returns clusterName as a deterministic
// fallback; that happens only for clusters absent from discovery, which have no client and
// never reach pod construction.
func ResourceNameForCluster(clusterName string) string {
	resourceNamesMu.RLock()
	defer resourceNamesMu.RUnlock()
	if name, ok := resourceNames[clusterName]; ok {
		return name
	}
	return clusterName
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

// Name returns the ServiceAccount name for this workload on the given cluster (identified by
// its spec.clusterName). baseInstall selects the fixed SA name provided by the base installation
// (helm/OLM); otherwise the member-scoped mck-member-<metadata.name>-<suffix> name is used.
func (w workloadServiceAccount) Name(clusterName string, baseInstall bool) string {
	if baseInstall {
		return w.fixedName
	}
	return MemberClusterResourceName(ResourceNameForCluster(clusterName)) + "-" + w.suffix
}
