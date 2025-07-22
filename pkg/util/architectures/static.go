package architectures

import (
	"strings"

	"k8s.io/utils/env"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

type DefaultArchitecture string

type ImageType string

const (
	ImageTypeUBI8    ImageType = "ubi8"
	ImageTypeUBI9    ImageType = "ubi9"
	DefaultImageType ImageType = ImageTypeUBI8
)

func HasSupportedImageTypeSuffix(imageVersion string) (suffixFound bool, suffix string) {
	if strings.HasSuffix(imageVersion, string(ImageTypeUBI8)) {
		return true, string(ImageTypeUBI8)
	}
	if strings.HasSuffix(imageVersion, string(ImageTypeUBI9)) {
		return true, string(ImageTypeUBI9)
	}
	return false, ""
}

const (
	ArchitectureAnnotation                     = "mongodb.com/v1.architecture"
	DefaultEnvArchitecture                     = "MDB_DEFAULT_ARCHITECTURE"
	Static                 DefaultArchitecture = "static"
	NonStatic              DefaultArchitecture = "non-static"
	// MdbAgentImageRepo contains the repository containing the agent image for the database
	MdbAgentImageRepo        = "MDB_AGENT_IMAGE_REPOSITORY"
	MdbAgentImageRepoDefault = "quay.io/mongodb/mongodb-agent-ubi"
)

// IsRunningStaticArchitecture checks whether the operator is running in static or non-static mode.
// This is either decided via an annotation per resource or per operator level.
// The resource annotation takes precedence.
// A nil map is equivalent to an empty map except that no elements may be added.
func IsRunningStaticArchitecture(annotations map[string]string) bool {
	if annotations != nil {
		if architecture, ok := annotations[ArchitectureAnnotation]; ok {
			if architecture == string(Static) {
				return true
			}
			if architecture == string(NonStatic) {
				return false
			}
		}
	}

	operatorEnv := env.GetString(DefaultEnvArchitecture, string(NonStatic))
	return operatorEnv == string(Static)
}

func GetArchitecture(annotations map[string]string) DefaultArchitecture {
	if IsRunningStaticArchitecture(annotations) {
		return Static
	}
	return NonStatic
}

// GetMongoVersionForAutomationConfig returns the required version with potentially the suffix -ent.
// If we are in static containers architecture, we need the -ent suffix in case we are running the ea image.
// If not, the agent will try to change the version to reflect the non-enterprise image.
func GetMongoVersionForAutomationConfig(mongoDBImage, version string, architecture DefaultArchitecture) string {
	if architecture != Static {
		return version
	}
	// the image repo should be	either mongodb / mongodb-enterprise-server or mongodb / mongodb-community-server
	if strings.Contains(mongoDBImage, util.OfficialEnterpriseServerImageUrl) {
		if !strings.HasSuffix(version, "-ent") {
			version = version + "-ent"
		}
	}

	return version
}
