package resourcenames

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Names in this file are for resources that live on the central (operator) cluster.

const (
	// memberClusterCredentialSecretPrefix prefixes the per-cluster credential Secret name.
	memberClusterCredentialSecretPrefix = "mck-credential-" //nolint:gosec // It is a credential name prefix, not a secret value.

	// maxMemberClusterNameLength caps a derived member cluster name so that the credential
	// Secret name (the prefix plus the derived name) stays within the 253-char DNS subdomain limit.
	maxMemberClusterNameLength = 253 - len(memberClusterCredentialSecretPrefix)

	// memberClusterNameHashLength is how many hex chars of the logical name's sha256 are
	// appended to a normalized name to keep it unique per logical name.
	memberClusterNameHashLength = 8
)

// MemberClusterCredentialSecretName returns the name of the per-cluster credential Secret placed on the
// central (operator) cluster.
func MemberClusterCredentialSecretName(memberClusterName string) string {
	return memberClusterCredentialSecretPrefix + memberClusterName
}

// NormalizeMemberClusterName derives the central-cluster resource names (the MemberCluster CR's
// metadata.name and the credential Secret name suffix) from a member cluster's logical name. It is
// a CLI convenience: hand-written or GitOps manifests are not bound by it and may use any unique
// RFC 1123 metadata.name with an explicit credentialSecretRef.
//
// A logical name that is already an RFC 1123 (DNS subdomain) compliant name within the length
// budget passes through unchanged. Any other name is normalized — lowercased, every character
// outside [a-z0-9.-] replaced with '-', then split into dot-separated labels with each label
// trimmed of leading/trailing '-' and empty labels dropped, so every label starts and ends with
// an alphanumeric as DNS subdomains require — and suffixed with
// the first 8 hex chars of the sha256 of the original logical name. The hash keeps the derived
// name unique when distinct logical names (or a normalized and a compliant name) would otherwise
// collide; the CLI is offline and per-cluster, so it cannot detect cross-cluster collisions
// itself. Names are capped at maxMemberClusterNameLength: on the hash path the normalized base is
// truncated so base+"-"+hash fits the budget, trimming trailing '-'/'.' after truncation.
//
// It returns an error when the logical name has no alphanumeric characters to derive a name from.
func NormalizeMemberClusterName(logicalName string) (string, error) {
	if len(logicalName) <= maxMemberClusterNameLength && len(validation.IsDNS1123Subdomain(logicalName)) == 0 {
		return logicalName, nil
	}

	normalized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, logicalName)
	// DNS subdomains require each dot-separated label to start and end with an alphanumeric,
	// so internal separator runs must be cleaned per label, not just at the string edges.
	labels := strings.Split(normalized, ".")
	kept := make([]string, 0, len(labels))
	for _, label := range labels {
		if label = strings.Trim(label, "-"); label != "" {
			kept = append(kept, label)
		}
	}
	normalized = strings.Join(kept, ".")
	if normalized == "" {
		return "", xerrors.Errorf("cannot derive a resource name from member cluster name %q: it contains no alphanumeric characters", logicalName)
	}

	hash := sha256.Sum256([]byte(logicalName))
	suffix := hex.EncodeToString(hash[:])[:memberClusterNameHashLength]

	if maxBaseLength := maxMemberClusterNameLength - 1 - memberClusterNameHashLength; len(normalized) > maxBaseLength {
		normalized = strings.TrimRight(normalized[:maxBaseLength], "-.")
	}
	derived := normalized + "-" + suffix
	if errs := validation.IsDNS1123Subdomain(derived); len(errs) > 0 {
		return "", xerrors.Errorf("cannot derive a valid RFC 1123 resource name from member cluster name %q (got %q): %v", logicalName, derived, errs)
	}
	return derived, nil
}
