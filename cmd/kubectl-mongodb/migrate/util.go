package migrate

import (
	"regexp"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"k8s.io/apimachinery/pkg/util/validation"
)

var (
	reNonAlphanumDash = regexp.MustCompile("[^a-z0-9-]+")
	reMultipleDash    = regexp.MustCompile(`-+`)
)

func pluginVersion() string {
	if util.OperatorVersion != "" {
		return util.OperatorVersion
	}
	return "latest"
}

// normalizeK8sName returns a string that conforms to RFC-1123.
func normalizeK8sName(name string) string {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) == 0 {
		return name
	}

	name = strings.ToLower(name)
	name = reNonAlphanumDash.ReplaceAllString(name, "-")
	name = reMultipleDash.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")

	if len(name) > validation.DNS1123SubdomainMaxLength {
		name = name[:validation.DNS1123SubdomainMaxLength]
	}
	return name
}
