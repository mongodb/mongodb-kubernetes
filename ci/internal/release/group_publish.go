package release

import (
	"errors"
	"fmt"
)

// GroupPublishResult records what was (or would be) published for one image.
type GroupPublishResult struct {
	Name        string   // build_info.json image key
	StagingRepo string   // primary staging repository the promoted image was read from
	ProdRepo    string   // production repository (release.repository) the tags were applied to
	Commit      string   // commit resolved from the promoted tag
	Version     string   // version resolved from the promoted tag
	Tags        []string // tags applied in the production repository: {version} and latest
}

// PublishGroup publishes every image in the group at the given commit: it
// resolves each image's promoted-{commit}-{version} candidate in its primary
// staging repository and retags it in its production (release.repository)
// registry as :{version} and :latest.
//
// commit is required (unlike the single-image publish command, which can fall
// back to promoted-latest): a group publish must publish one specific,
// already-promoted commit consistently across all six images, not whatever
// each image's staging repo independently considers latest.
//
// It hard-fails on the first image that cannot be published, so a partial or
// broken promotion does not silently publish an inconsistent group. connect
// resolves a Registry for a registry host; the CLI passes
// DefaultRegistryConnector and tests inject a fake. The staging and
// production repositories for an image may live on different hosts (e.g. ECR
// staging, quay.io production); connect is called with the production host,
// since that is where the tags are ultimately applied.
func PublishGroup(images []ReleaseImage, commit string, dryRun bool, connect RegistryConnector) ([]GroupPublishResult, error) {
	if commit == "" {
		return nil, errors.New("commit is required")
	}
	if len(images) == 0 {
		return nil, errors.New("no images to publish")
	}

	results := make([]GroupPublishResult, 0, len(images))
	for _, img := range images {
		host, path := splitHostRepo(img.ReleaseRepo)
		result, err := Publish(PublishInputs{
			StagingImage: img.StagingRepo,
			Commit:       commit,
			ProdRepo:     path,
			DryRun:       dryRun,
		}, connect(host))
		if err != nil {
			return nil, fmt.Errorf("publish %s (%s): %w", img.Name, img.StagingRepo, err)
		}
		results = append(results, GroupPublishResult{
			Name:        img.Name,
			StagingRepo: img.StagingRepo,
			ProdRepo:    img.ReleaseRepo,
			Commit:      result.Commit,
			Version:     result.Version,
			Tags:        result.AppliedTags,
		})
	}
	return results, nil
}
