package release

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// AnchorImageName is the image that anchors the release group. The operator is
// the one image built at merge time; the rest travel with its commit. Its
// presence in the group is required. (Publish-time "latest" resolution keys off
// this same anchor, but that lives in a separate change.)
const AnchorImageName = "operator"

// ReleaseImage is one member of the release group, resolved from build_info.json
// (repositories + version-ref) and release.json (the concrete version).
type ReleaseImage struct {
	Name             string   // build_info.json image key, e.g. "readiness-probe"
	StagingRepo      string   // staging.repository
	StagingSecondary []string // staging.secondary-repositories (may be empty)
	ReleaseRepo      string   // release.repository
	VersionRef       string   // build_info.json version-ref, a key into release.json
	Version          string   // release.json[VersionRef]
	IsAnchor         bool     // Name == AnchorImageName
}

// buildInfoFile is the minimal shape of build_info.json this package reads.
type buildInfoFile struct {
	Images map[string]struct {
		VersionRef string `json:"version-ref"`
		Staging    struct {
			Repository string   `json:"repository"`
			Secondary  []string `json:"secondary-repositories"`
		} `json:"staging"`
		Release struct {
			Repository string `json:"repository"`
		} `json:"release"`
	} `json:"images"`
}

// LoadReleaseImages reads build_info.json and release.json and returns the
// release group: exactly the images carrying a version-ref. Membership is
// version-ref presence alone, which excludes test images and the separately
// released agent/ops-manager images. Each member is validated (staging repo,
// release repo, and a resolvable version); any gap is a hard error, so a
// misconfigured build_info.json fails loudly rather than silently dropping or
// mis-versioning an image. The result is sorted by Name for determinism.
func LoadReleaseImages(buildInfoPath, releaseJSONPath string) ([]ReleaseImage, error) {
	var bi buildInfoFile
	if err := readJSON(buildInfoPath, &bi); err != nil {
		return nil, fmt.Errorf("read build info %s: %w", buildInfoPath, err)
	}

	var versions map[string]any
	if err := readJSON(releaseJSONPath, &versions); err != nil {
		return nil, fmt.Errorf("read release versions %s: %w", releaseJSONPath, err)
	}

	var images []ReleaseImage
	for name, img := range bi.Images {
		if img.VersionRef == "" {
			continue
		}
		if img.Staging.Repository == "" {
			return nil, fmt.Errorf("image %q has version-ref but no staging.repository", name)
		}
		if img.Release.Repository == "" {
			return nil, fmt.Errorf("image %q has version-ref but no release.repository", name)
		}
		version, err := lookupVersion(versions, img.VersionRef)
		if err != nil {
			return nil, fmt.Errorf("image %q: %w", name, err)
		}
		images = append(images, ReleaseImage{
			Name:             name,
			StagingRepo:      img.Staging.Repository,
			StagingSecondary: img.Staging.Secondary,
			ReleaseRepo:      img.Release.Repository,
			VersionRef:       img.VersionRef,
			Version:          version,
			IsAnchor:         name == AnchorImageName,
		})
	}

	if len(images) == 0 {
		return nil, fmt.Errorf("no release images (none carry a version-ref) in %s", buildInfoPath)
	}

	hasAnchor := false
	for _, img := range images {
		if img.IsAnchor {
			hasAnchor = true
			break
		}
	}
	if !hasAnchor {
		return nil, fmt.Errorf("anchor image %q not found in release group", AnchorImageName)
	}

	sort.Slice(images, func(i, j int) bool { return images[i].Name < images[j].Name })
	return images, nil
}

func lookupVersion(versions map[string]any, ref string) (string, error) {
	raw, ok := versions[ref]
	if !ok {
		return "", fmt.Errorf("version-ref %q not found in release versions", ref)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("version-ref %q is not a string in release versions", ref)
	}
	if s == "" {
		return "", fmt.Errorf("version-ref %q is empty in release versions", ref)
	}
	return s, nil
}

func readJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}
