package images

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/envvar"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

// replaceImageTagOrDigestToTag returns the image with the tag or digest replaced to a given version
func replaceImageTagOrDigestToTag(image string, newVersion string) string {
	// example: quay.io/mongodb/mongodb-agent@sha256:6a82abae27c1ba1133f3eefaad71ea318f8fa87cc57fe9355d6b5b817ff97f1a
	if strings.Contains(image, "sha256:") {
		imageSplit := strings.Split(image, "@")
		imageSplit[len(imageSplit)-1] = newVersion
		return strings.Join(imageSplit, ":")
	} else {
		// examples:
		//  - quay.io/mongodb/mongodb-agent:1234-567
		//  - private-registry.local:3000/mongodb/mongodb-agent:1234-567
		//  - mongodb
		idx := strings.IndexRune(image, '/')
		// If there is no domain separator in the image string or the segment before the slash does not contain
		// a '.' or ':' and is not 'localhost' to indicate that the segment is a host, assume the image will be pulled from
		// docker.io and the whole string represents the image.
		if idx == -1 || (!strings.ContainsAny(image[:idx], ".:") && image[:idx] != "localhost") {
			return fmt.Sprintf("%s:%s", image[:strings.LastIndex(image, ":")], newVersion)
		}

		host := image[:idx]
		imagePath := image[idx+1:]

		// If there is a ':' in the image path we can safely assume that it is a version separator.
		if strings.Contains(imagePath, ":") {
			imagePath = imagePath[:strings.LastIndex(imagePath, ":")]
		}
		return fmt.Sprintf("%s/%s:%s", host, imagePath, newVersion)
	}
}

// ImageUrls is a map of image names to their corresponding image URLs.
type ImageUrls map[string]string

// LoadImageUrlsFromEnv reads all environment variables and selects
// the ones that are related to images for workloads. This includes env vars
// with RELATED_IMAGE_ prefix.
// RELATED_IMAGE_* env variables are set in Helm chart for OpenShift.
func LoadImageUrlsFromEnv() ImageUrls {
	imageUrls := make(ImageUrls)

	for imageName, defaultValue := range map[string]string{
		// If you are considering adding a new variable here
		// you at least one of the following should be true:
		//   - New env var has a RELATED_IMAGE_* counterpart
		//   - New env var contains an image URL or part of the URL
		//     and it will be used in container for MongoDB workfloads
		construct.MongodbRepoUrlEnv:           "",
		construct.MongodbImageEnv:             "",
		util.InitOpsManagerImageUrl:           "",
		util.OpsManagerImageUrl:               "",
		util.InitDatabaseImageUrlEnv:          "",
		util.InitAppdbImageUrlEnv:             "",
		util.NonStaticDatabaseEnterpriseImage: "",
		construct.AgentImageEnv:               "",
		architectures.MdbAgentImageRepo:       architectures.MdbAgentImageRepoDefault,
	} {
		imageUrls[imageName] = env.ReadOrDefault(imageName, defaultValue) // nolint:forbidigo
	}

	for _, env := range os.Environ() { // nolint:forbidigo
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			// Should never happen because os.Environ() returns key=value pairs
			// but we are being defensive here.
			continue
		}

		if strings.HasPrefix(parts[0], "RELATED_IMAGE_") {
			imageUrls[parts[0]] = parts[1]
		}
	}
	return imageUrls
}

// ContainerImage selects container image from imageUrls.
// It handles image digests when running in disconnected environment in OpenShift, where images
// are referenced by sha256 digest instead of tags.
// It works by convention by looking up RELATED_IMAGE_{imageName}_{versionUnderscored}.
// In case there is no RELATED_IMAGE_ defined it replaces digest or tag to version.
func ContainerImage(imageUrls ImageUrls, imageName string, version string) string {
	versionUnderscored := strings.ReplaceAll(version, ".", "_")
	versionUnderscored = strings.ReplaceAll(versionUnderscored, "-", "_")
	relatedImageKey := fmt.Sprintf("RELATED_IMAGE_%s_%s", imageName, versionUnderscored)

	if relatedImage, ok := imageUrls[relatedImageKey]; ok {
		return relatedImage
	}

	imageURL := imageUrls[imageName]

	if strings.Contains(imageURL, ":") {
		// here imageURL is not a host only but also with version or digest
		// in that case we need to replace the version/digest.
		// This is case with AGENT_IMAGE env variable which is provided as a full URL with version
		// and not as a pair of host and version.
		// In case AGENT_IMAGE is full URL with digest it will be replaced to given tag version,
		// but most probably if it has digest, there is also RELATED_IMAGE defined, which will be picked up first.
		return replaceImageTagOrDigestToTag(imageURL, version)
	}

	return fmt.Sprintf("%s:%s", imageURL, version)
}

func GetOfficialImage(imageUrls ImageUrls, version string, annotations map[string]string) string {
	repoUrl := imageUrls[construct.MongodbRepoUrlEnv]
	// TODO: rethink the logic of handling custom image types. We are currently only handling ubi9 and ubi8 and we never
	// were really handling erroneus types, we just leave them be if specified (e.g. -ubuntu).
	// envvar.GetEnvOrDefault(construct.MongoDBImageType, string(architectures.DefaultImageType))
	var imageType string

	if architectures.IsRunningStaticArchitecture(annotations) {
		imageType = string(architectures.ImageTypeUBI9)
	} else {
		// For non-static architecture, we need to default to UBI8 to support customers running MongoDB versions < 6.0.4,
		// which don't have UBI9 binaries.
		imageType = string(architectures.ImageTypeUBI8)
	}

	imageURL := imageUrls[construct.MongodbImageEnv]

	if strings.HasSuffix(repoUrl, "/") {
		repoUrl = strings.TrimRight(repoUrl, "/")
	}

	assumeOldFormat := envvar.ReadBool(util.MdbAppdbAssumeOldFormat)
	if IsEnterpriseImage(imageURL) && !assumeOldFormat {
		// 5.0.6-ent -> 5.0.6-ubi8
		if strings.HasSuffix(version, "-ent") {
			version = fmt.Sprintf("%s%s", strings.TrimSuffix(version, "ent"), imageType)
		}
		// 5.0.6 ->  5.0.6-ubi8
		r := regexp.MustCompile("-.+$")
		if !r.MatchString(version) {
			version = version + "-" + imageType
		}
		if found, suffix := architectures.HasSupportedImageTypeSuffix(version); found {
			version = fmt.Sprintf("%s%s", strings.TrimSuffix(version, suffix), imageType)
		}
		// if neither, let's not change it: 5.0.6-ubi8 -> 5.0.6-ubi8
	}

	mongoImageName := ContainerImage(imageUrls, construct.MongodbImageEnv, version)

	if strings.Contains(mongoImageName, "@sha256:") || strings.HasPrefix(mongoImageName, repoUrl) {
		return mongoImageName
	}

	return fmt.Sprintf("%s/%s", repoUrl, mongoImageName)
}

func IsEnterpriseImage(mongodbImage string) bool {
	return strings.Contains(mongodbImage, util.OfficialEnterpriseServerImageUrl)
}
