package release

import (
	"errors"
	"fmt"
	"strings"
)

// GroupPromoteResult records what was (or would be) promoted for one image.
type GroupPromoteResult struct {
	Name    string   // build_info.json image key
	Repo    string   // primary staging repository (host/path) the tags were applied to
	Version string   // release.json version the source image was tagged with
	Tags    []string // tags applied: promoted-{commit}-{version} and promoted-latest
}

// PromoteGroup promotes every image in the group at the given commit, writing
// promoted-{commit}-{version} and promoted-latest to each image's PRIMARY
// staging repository only (secondary repositories are intentionally left
// untouched for now). The source image is the version-tagged image the staging
// build already pushed, e.g. <staging-repo>:<version>.
//
// It hard-fails on the first image that cannot be promoted (e.g. its
// version-tagged source image is missing), so a broken merge build does not
// silently promote a partial group. connect resolves a Registry for an image's
// host; the CLI passes DefaultRegistryConnector and tests inject a fake.
func PromoteGroup(images []ReleaseImage, commit string, dryRun bool, connect RegistryConnector) ([]GroupPromoteResult, error) {
	if commit == "" {
		return nil, errors.New("commit is required")
	}
	if len(images) == 0 {
		return nil, errors.New("no images to promote")
	}

	results := make([]GroupPromoteResult, 0, len(images))
	for _, img := range images {
		host, path := splitHostRepo(img.StagingRepo)
		src := fmt.Sprintf("%s:%s", img.StagingRepo, img.Version)
		tags, err := Promote(PromoteInputs{
			Image:   src,
			Commit:  commit,
			Version: img.Version,
			Repo:    path,
			DryRun:  dryRun,
		}, connect(host))
		if err != nil {
			return nil, fmt.Errorf("promote %s (%s): %w", img.Name, src, err)
		}
		results = append(results, GroupPromoteResult{
			Name:    img.Name,
			Repo:    img.StagingRepo,
			Version: img.Version,
			Tags:    tags,
		})
	}
	return results, nil
}

// splitHostRepo splits "host/path/to/repo" into ("host", "path/to/repo").
func splitHostRepo(repo string) (host, path string) {
	if i := strings.Index(repo, "/"); i >= 0 {
		return repo[:i], repo[i+1:]
	}
	return "", repo
}
