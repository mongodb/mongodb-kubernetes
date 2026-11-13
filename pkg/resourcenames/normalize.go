package resourcenames

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// maxMemberClusterNameLength caps a derived member cluster name, reserving 30 chars of
	// headroom for prefixes of resources that embed it (e.g. memberClusterCredentialSecretPrefix).
	maxMemberClusterNameLength = validation.DNS1123SubdomainMaxLength - 30

	// memberClusterNameHashLength is how many hex chars of the logical name's sha256 are
	// appended to a normalized name to keep it unique per logical name.
	memberClusterNameHashLength = 8
)

// NormalizeMemberClusterName derives an RFC 1123 resource name from a member cluster's logical
// name. A compliant name within maxMemberClusterNameLength passes through unchanged; any other
// is normalized per DNS label and suffixed with a short hash of the original name for
// deterministic uniqueness. It errors when the name contains no alphanumeric characters.
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
