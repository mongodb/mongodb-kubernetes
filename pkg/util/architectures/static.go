package architectures

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

type DefaultArchitecture string

type ImageType string

const (
	ImageTypeUBI8     ImageType = "ubi8"
	ImageTypeUBI9     ImageType = "ubi9"
	ImageTypeUBI9Slim ImageType = "ubi9-slim"
)

func HasSupportedImageTypeSuffix(imageVersion string) (suffixFound bool, suffix string) {
	if strings.HasSuffix(imageVersion, string(ImageTypeUBI9Slim)) {
		return true, string(ImageTypeUBI9Slim)
	}
	if strings.HasSuffix(imageVersion, string(ImageTypeUBI8)) {
		return true, string(ImageTypeUBI8)
	}
	if strings.HasSuffix(imageVersion, string(ImageTypeUBI9)) {
		return true, string(ImageTypeUBI9)
	}
	return false, ""
}

const (
	ArchitectureAnnotation                           = "mongodb.com/v1.architecture"
	ContainerImageTypeAnnotation                     = "mongodb.com/v1.containerImageType"
	DefaultEnvArchitecture                           = "MDB_DEFAULT_ARCHITECTURE"
	DefaultEnvImageType                              = "MDB_DEFAULT_IMAGE_TYPE"
	Static                       DefaultArchitecture = "static"
	NonStatic                    DefaultArchitecture = "non-static"
	// MdbAssumeEnterpriseImage allows the customer to override the version image detection used by the operator to
	// set up the automation config.
	// true: always append the -ent suffix and assume enterprise
	// false: do not append the -ent suffix and assume community
	// default: false
	MdbAssumeEnterpriseImage = "MDB_ASSUME_ENTERPRISE_IMAGE"
)

// IsRunningStaticArchitecture checks whether the operator is running in static or non-static mode.
// This is either decided via an annotation per resource or per operator level.
// The resource annotation takes precedence.
// A nil map is equivalent to an empty map except that no elements may be added.
func IsRunningStaticArchitecture(annotations map[string]string, defaultArchitecture DefaultArchitecture) bool {
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

	return defaultArchitecture == Static
}

func GetArchitecture(annotations map[string]string, defaultArchitecture DefaultArchitecture) DefaultArchitecture {
	if IsRunningStaticArchitecture(annotations, defaultArchitecture) {
		return Static
	}
	return NonStatic
}

func GetImageType(annotations map[string]string, defaultArchitecture DefaultArchitecture, defaultImageType ImageType) ImageType {
	if !IsRunningStaticArchitecture(annotations, defaultArchitecture) {
		return ImageTypeUBI8
	}

	if annotations != nil {
		if imageType := ImageType(annotations[ContainerImageTypeAnnotation]); IsSupportedImageType(imageType) {
			return imageType
		}
	}

	if IsSupportedImageType(defaultImageType) {
		return defaultImageType
	}

	return ImageTypeUBI9
}

func SupportedImageTypes() []ImageType {
	return []ImageType{ImageTypeUBI8, ImageTypeUBI9, ImageTypeUBI9Slim}
}

func IsSupportedImageType(imageType ImageType) bool {
	return slices.Contains(SupportedImageTypes(), imageType)
}

// ContainerImageTypeAnnotationWarning returns a user-facing warning when the ContainerImageTypeAnnotation is set
// on a resource but cannot be honored, either because its value is unsupported or because the resource is not
// running the static architecture. It returns an empty string when there is nothing to warn about.
func ContainerImageTypeAnnotationWarning(annotations map[string]string, defaultArchitecture DefaultArchitecture) string {
	value, ok := annotations[ContainerImageTypeAnnotation]
	if !ok {
		return ""
	}

	if !IsRunningStaticArchitecture(annotations, defaultArchitecture) {
		return fmt.Sprintf("Annotation %s=%q is ignored because the resource is not running the static architecture", ContainerImageTypeAnnotation, value)
	}

	if !IsSupportedImageType(ImageType(value)) {
		return fmt.Sprintf("Annotation %s=%q is ignored because the value is not supported (supported values: %s); the operator default image type is used instead", ContainerImageTypeAnnotation, value, supportedImageTypesString())
	}

	return ""
}

// ValidateDefaultImageType returns the default image type to use for the given configured value. If the value is
// unsupported, it falls back to ImageTypeUBI9 and returns a non-empty warning describing the fallback.
func ValidateDefaultImageType(value string) (ImageType, string) {
	if IsSupportedImageType(ImageType(value)) {
		return ImageType(value), ""
	}
	return ImageTypeUBI9, fmt.Sprintf("Unsupported value %q for %s (supported values: %s); falling back to %q", value, DefaultEnvImageType, supportedImageTypesString(), ImageTypeUBI9)
}

func supportedImageTypesString() string {
	supported := make([]string, 0, len(SupportedImageTypes()))
	for _, imageType := range SupportedImageTypes() {
		supported = append(supported, string(imageType))
	}
	return strings.Join(supported, ", ")
}

// GetMongoVersionForAutomationConfig returns the required version with potentially the suffix -ent.
// If we are in static containers architecture, we need the -ent suffix in case we are running the ea image.
// If not, the agent will try to change the version to reflect the non-enterprise image.
func GetMongoVersionForAutomationConfig(mongoDBImage, version string, forceEnterprise bool, architecture DefaultArchitecture) string {
	if architecture != Static {
		return version
	}
	// the image repo should be	either mongodb / mongodb-enterprise-server or mongodb / mongodb-community-server
	if strings.Contains(mongoDBImage, util.OfficialEnterpriseServerImageName) || forceEnterprise {
		if !strings.HasSuffix(version, "-ent") {
			version = version + "-ent"
		}
	}

	return version
}
