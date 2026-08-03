package resourcenames

// Names in this file are for resources that live on the central (operator) cluster.

const (
	// memberClusterCredentialSecretPrefix prefixes the per-cluster credential Secret name.
	memberClusterCredentialSecretPrefix = "mck-credential-" //nolint:gosec // It is a credential name prefix, not a secret value.
)

// MemberClusterCredentialSecretName returns the name of the per-cluster credential Secret placed on the
// central (operator) cluster.
func MemberClusterCredentialSecretName(memberClusterName string) string {
	return memberClusterCredentialSecretPrefix + memberClusterName
}
