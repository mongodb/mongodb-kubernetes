package release

import (
	"errors"
	"fmt"
	"strings"
)

// GroupPublishResult records what was (or would be) published for one image.
type GroupPublishResult struct {
	Name        string   // build_info.json image key
	StagingRepo string   // primary staging repository the promoted image was read from
	ProdRepo    string   // production repository (release.repository) the tags were applied to
	Commit      string   // commit resolved from the promoted tag
	Version     string   // version resolved from the promoted tag
	Tags        []string // tags applied in the production repository: {version} and latest
	Warnings    []string
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
// Stomping protection is applied group-wide, not per image: every image's
// immutable :{version} production tag is checked for conflicts BEFORE any
// writes happen. If any image conflicts and force is false, the whole group
// is refused untouched. With force, every image proceeds.
//
// It hard-fails on the first image that cannot be published, so a partial or
// broken promotion does not silently publish an inconsistent group. connect
// resolves a Registry for a registry host; the CLI passes
// DefaultRegistryConnector and tests inject a fake. The staging and
// production repositories for an image may live on different hosts (e.g. ECR
// staging, quay.io production); connect is called with the production host,
// since that is where the tags are ultimately applied.
func PublishGroup(images []ReleaseImage, commit string, force, dryRun bool, connect RegistryConnector) ([]GroupPublishResult, error) {
	if commit == "" {
		return nil, errors.New("commit is required")
	}
	if len(images) == 0 {
		return nil, errors.New("no images to publish")
	}

	type prepared struct {
		img      ReleaseImage
		reg      Registry
		host     string
		prodPath string
		version  string
	}

	prep := make([]prepared, 0, len(images))
	var conflicts []string
	for _, img := range images {
		host, path := splitHostRepo(img.ReleaseRepo)
		reg := connect(host)

		_, version, err := resolvePromoted(img.StagingRepo, commit, reg)
		if err != nil {
			return nil, fmt.Errorf("resolve %s (%s): %w", img.Name, img.StagingRepo, err)
		}
		srcRef := fmt.Sprintf("%s:%s", img.StagingRepo, PromotedTagFor(commit, version))
		dstVersionRef := fmt.Sprintf("%s/%s:%s", host, path, version)

		conflict, _, err := checkStomp(reg, srcRef, dstVersionRef)
		if err != nil {
			return nil, fmt.Errorf("check %s (%s): %w", img.Name, srcRef, err)
		}
		if conflict != "" {
			conflicts = append(conflicts, fmt.Sprintf("%s: %s", img.Name, conflict))
		}
		prep = append(prep, prepared{img: img, reg: reg, host: host, prodPath: path, version: version})
	}
	if len(conflicts) > 0 && !force {
		return nil, fmt.Errorf("refusing to publish group; tag conflicts found (use --force to override):\n%s", strings.Join(conflicts, "\n"))
	}

	results := make([]GroupPublishResult, 0, len(images))
	for _, p := range prep {
		result, err := Publish(PublishInputs{
			StagingImage: p.img.StagingRepo,
			Commit:       commit,
			ProdRepo:     p.prodPath,
			// The group-wide check above already gated on conflicts; each
			// image proceeds regardless of its individual outcome.
			Force:  true,
			DryRun: dryRun,
		}, p.host, p.reg)
		if err != nil {
			return nil, fmt.Errorf("publish %s (%s): %w", p.img.Name, p.img.StagingRepo, err)
		}
		results = append(results, GroupPublishResult{
			Name:        p.img.Name,
			StagingRepo: p.img.StagingRepo,
			ProdRepo:    p.img.ReleaseRepo,
			Commit:      result.Commit,
			Version:     result.Version,
			Tags:        result.AppliedTags,
			Warnings:    result.Warnings,
		})
	}
	return results, nil
}
