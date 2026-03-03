package migrate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"k8s.io/apimachinery/pkg/util/validation"
)

func pluginVersion() string {
	if util.OperatorVersion != "" {
		return util.OperatorVersion
	}
	return "latest"
}

// normalizeK8sName returns a string that conforms to RFC-1123.
// Copied from the enterprise operator: api/v1/user/mongodbuser_types.go (normalizeName).
func normalizeK8sName(name string) string {
	errors := validation.IsDNS1123Subdomain(name)
	if len(errors) == 0 {
		return name
	}

	name = strings.ToLower(name)
	re := regexp.MustCompile("[^a-z0-9-]+")
	name = re.ReplaceAllString(name, "-")

	re = regexp.MustCompile(`\-+`)
	name = re.ReplaceAllString(name, "-")

	name = strings.Trim(name, "-")

	if len(name) > validation.DNS1123SubdomainMaxLength {
		name = name[0:validation.DNS1123SubdomainMaxLength]
	}
	return name
}

func appendExternalMembersComment(yamlStr string, externalMembers []ExternalMember) string {
	if len(externalMembers) == 0 {
		return yamlStr
	}

	var sb strings.Builder
	sb.WriteString(yamlStr)
	sb.WriteString("  # externalMembers will be populated by the operator with the current VM members:\n")
	sb.WriteString("  # externalMembers:\n")
	for _, em := range externalMembers {
		sb.WriteString(fmt.Sprintf("  #   - hostname: %q\n", em.Hostname))
		sb.WriteString(fmt.Sprintf("  #     port: %d\n", em.Port))
		sb.WriteString(fmt.Sprintf("  #     votes: %d\n", em.Votes))
		sb.WriteString(fmt.Sprintf("  #     priority: %g\n", em.Priority))
		if em.ArbiterOnly {
			sb.WriteString("  #     arbiterOnly: true\n")
		}
	}

	return sb.String()
}
