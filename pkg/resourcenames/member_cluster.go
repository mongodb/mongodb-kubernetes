// Package resourcenames centralises the names of resources used for MCK multi-cluster
// configuration. These names form a contract shared by the kubectl plugin and the operator.
//
// The <member-cluster> segment used by the functions in this package is the RFC 1123 name
// (MemberCluster CR metadata.name), not the logical spec.clusterName.
package resourcenames

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
