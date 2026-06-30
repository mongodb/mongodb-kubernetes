package release

import (
	"errors"
	"fmt"
	"strings"
)

// PublishInputs are the parameters for a publish operation.
type PublishInputs struct {
	// StagingImage is the base repo path for the staging registry,
	// e.g. "quay.io/mongodb/staging/mongodb-kubernetes". Must not include a tag.
	StagingImage string
	// ProdRepo is the target repository in the production registry,
	// e.g. "mongodb/mongodb-kubernetes-operator".
	ProdRepo string
	// Commit is the SHA of the promoted commit to publish.
	// If empty, it is resolved from the promoted-latest tag.
	Commit string
	// DryRun prints what would happen without copying any images.
	DryRun bool
}

// PublishResult holds the resolved inputs and the tags that were (or would be) applied.
type PublishResult struct {
	Commit      string
	Version     string
	AppliedTags []string
}

// Publish assumes the staging and production repositories live on the same
// registry host (the one reg was connected to). The host embedded in
// StagingImage is only used to strip the repo path; tag listing and copying
// both target reg's host.
func Publish(inputs PublishInputs, reg Registry) (PublishResult, error) {
	if inputs.StagingImage == "" {
		return PublishResult{}, errors.New("staging-image is required")
	}
	if inputs.ProdRepo == "" {
		return PublishResult{}, errors.New("prod-repo is required")
	}

	commit, version, err := resolvePromoted(inputs.StagingImage, inputs.Commit, reg)
	if err != nil {
		return PublishResult{}, err
	}

	tags := []string{version, "latest"}
	result := PublishResult{Commit: commit, Version: version, AppliedTags: tags}

	if inputs.DryRun {
		return result, nil
	}

	srcRef := fmt.Sprintf("%s:%s", inputs.StagingImage, PromotedTagFor(commit, version))
	if err := reg.CopyWithTags(srcRef, inputs.ProdRepo, tags); err != nil {
		return PublishResult{}, fmt.Errorf("publish %s: %w", srcRef, err)
	}
	return result, nil
}

func resolvePromoted(stagingImage, commit string, reg Registry) (string, string, error) {
	tags, err := reg.ListTags(stagingImage)
	if err != nil {
		return "", "", fmt.Errorf("list tags for %s: %w", stagingImage, err)
	}

	if commit == "" {
		latestRef := fmt.Sprintf("%s:%s", stagingImage, promotedLatestTag())
		latestDigest, err := reg.GetDigest(latestRef)
		if err != nil {
			return "", "", fmt.Errorf("resolve promoted-latest: %w", err)
		}
		for _, tag := range tags {
			if !isPromotedVersionTag(tag) {
				continue
			}
			d, err := reg.GetDigest(fmt.Sprintf("%s:%s", stagingImage, tag))
			if err != nil {
				continue
			}
			if d == latestDigest {
				return parsePromotedTag(tag)
			}
		}
		return "", "", fmt.Errorf("no promoted-{commit}-{version} tag matches promoted-latest in %s", stagingImage)
	}

	prefix := PromotedTagPrefix + commit + "-"
	for _, tag := range tags {
		if strings.HasPrefix(tag, prefix) && isPromotedVersionTag(tag) {
			return parsePromotedTag(tag)
		}
	}
	return "", "", fmt.Errorf("no promoted tag found for commit %s in %s", commit, stagingImage)
}

func isPromotedVersionTag(tag string) bool {
	return strings.HasPrefix(tag, PromotedTagPrefix) && tag != promotedLatestTag()
}

func parsePromotedTag(tag string) (string, string, error) {
	rest := strings.TrimPrefix(tag, PromotedTagPrefix)
	// A commit SHA is hex, so the first dash after the prefix separates the
	// commit from the version. Splitting here (rather than at a fixed 40-char
	// offset) accepts commits of any length and versions that contain dashes
	// (e.g. "1.0.0-rc1").
	commit, version, found := strings.Cut(rest, "-")
	if !found || commit == "" || version == "" {
		return "", "", fmt.Errorf("malformed promoted tag: %q", tag)
	}
	return commit, version, nil
}
