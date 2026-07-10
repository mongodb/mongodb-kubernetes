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
	// Force allows overwriting the immutable :{version} production tag even if
	// it already points at a different digest. Without it, such a conflict is
	// a hard error (image stomping protection).
	Force bool
	// DryRun prints what would happen without copying any images.
	DryRun bool
}

// PublishResult holds the resolved inputs and the tags that were (or would be) applied.
type PublishResult struct {
	Commit      string
	Version     string
	AppliedTags []string
	Warnings    []string
}

// Publish retags a promoted staging candidate as :{version} and :latest in
// the production registry. host is the destination (production) registry
// host (e.g. "quay.io"); inputs.ProdRepo is a path relative to it, matching
// Registry.CopyWithTags' own convention. StagingImage, by contrast, is always
// fully-qualified (may live on a different host, e.g. ECR staging vs quay.io
// production).
func Publish(inputs PublishInputs, host string, reg Registry) (PublishResult, error) {
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

	srcRef := fmt.Sprintf("%s:%s", inputs.StagingImage, PromotedTagFor(commit, version))
	dstVersionRef := fmt.Sprintf("%s/%s:%s", host, inputs.ProdRepo, version)

	// Only the immutable :{version} tag is stomp-checked; :latest is a mutable
	// pointer that is meant to move on every publish.
	conflict, upToDate, err := checkStomp(reg, srcRef, dstVersionRef)
	if err != nil {
		return PublishResult{}, err
	}
	if conflict != "" && !inputs.Force {
		return PublishResult{}, fmt.Errorf("refusing to publish: %s (use --force to override)", conflict)
	}

	var warnings []string
	if upToDate {
		warnings = append(warnings, fmt.Sprintf("%s already exists at the same digest", dstVersionRef))
	} else if conflict != "" {
		warnings = append(warnings, fmt.Sprintf("overwriting due to --force: %s", conflict))
	}

	tags := []string{version, "latest"}
	result := PublishResult{Commit: commit, Version: version, AppliedTags: tags, Warnings: warnings}

	if inputs.DryRun {
		return result, nil
	}

	// Immutable tag first: only once that succeeds do we move :latest, so a
	// failure never leaves latest ahead of the version it's supposed to point at.
	if err := reg.CopyWithTags(srcRef, inputs.ProdRepo, []string{version}); err != nil {
		return PublishResult{}, fmt.Errorf("publish %s: %w", srcRef, err)
	}
	if err := reg.CopyWithTags(srcRef, inputs.ProdRepo, []string{"latest"}); err != nil {
		return PublishResult{}, fmt.Errorf("publish %s (latest): %w", srcRef, err)
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
		latestDigest, err := reg.Digest(latestRef)
		if err != nil {
			return "", "", fmt.Errorf("resolve promoted-latest: %w", err)
		}
		for _, tag := range tags {
			if !isPromotedVersionTag(tag) {
				continue
			}
			d, err := reg.Digest(fmt.Sprintf("%s:%s", stagingImage, tag))
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
