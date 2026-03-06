package migrate

import (
	"fmt"
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
func normalizeK8sName(name string) (string, error) {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) == 0 {
		return name, nil
	}

	name = strings.ToLower(name)
	name = reNonAlphanumDash.ReplaceAllString(name, "-")
	name = reMultipleDash.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")

	if name == "" {
		return "", fmt.Errorf("cannot normalize %q to a valid Kubernetes name: no alphanumeric characters", name)
	}

	if len(name) > validation.DNS1123SubdomainMaxLength {
		name = name[:validation.DNS1123SubdomainMaxLength]
	}
	return name, nil
}
