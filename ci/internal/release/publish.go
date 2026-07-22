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
	// If empty, it is resolved from the promoted-{marker} tag (see LatestMarker).
	Commit string
	// LatestMarker customizes the mutable :{marker} production tag (and,
	// correspondingly, which promoted-{marker} staging tag is used to
	// resolve Commit when it is empty). Required; the CLI defaults it to
	// "latest" via the --latest-marker flag. Backport releases (e.g.
	// publishing a patch on an older branch after a newer version has
	// already been published) should pass a distinct marker (e.g.
	// "latestv1") so they don't move ":latest" away from the newest release.
	LatestMarker string
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
	Infos       []string
}

// Publish retags a promoted staging candidate as :{version} and :{marker}
// (marker defaults to "latest", see PublishInputs.LatestMarker) in the
// production registry. host is the destination (production) registry
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
	if inputs.LatestMarker == "" {
		return PublishResult{}, errors.New("latest-marker is required")
	}

	marker := inputs.LatestMarker
	commit, version, err := resolvePromoted(inputs.StagingImage, inputs.Commit, marker, reg)
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

	var warnings, infos []string
	if upToDate {
		infos = append(infos, fmt.Sprintf("%s is already in place", dstVersionRef))
	} else if conflict != "" {
		warnings = append(warnings, fmt.Sprintf("overwriting due to --force: %s", conflict))
	}

	tags := []string{version, marker}
	result := PublishResult{Commit: commit, Version: version, AppliedTags: tags, Warnings: warnings, Infos: infos}

	if inputs.DryRun {
		return result, nil
	}

	// Immutable tag first: only once that succeeds do we move :{marker}, so a
	// failure never leaves it ahead of the version it's supposed to point at.
	if err := reg.CopyWithTags(srcRef, inputs.ProdRepo, []string{version}); err != nil {
		return PublishResult{}, fmt.Errorf("publish %s: %w", srcRef, err)
	}
	if err := reg.CopyWithTags(srcRef, inputs.ProdRepo, []string{marker}); err != nil {
		return PublishResult{}, fmt.Errorf("publish %s (%s): %w", srcRef, marker, err)
	}
	return result, nil
}

func resolvePromoted(stagingImage, commit, marker string, reg Registry) (string, string, error) {
	tags, err := reg.ListTags(stagingImage)
	if err != nil {
		return "", "", fmt.Errorf("list tags for %s: %w", stagingImage, err)
	}

	if commit == "" {
		latestRef := fmt.Sprintf("%s:%s", stagingImage, promotedTagFor(marker))
		latestDigest, err := reg.Digest(latestRef)
		if err != nil {
			return "", "", fmt.Errorf("resolve promoted-%s: %w", marker, err)
		}
		for _, tag := range tags {
			if !isPromotedVersionTag(tag, marker) {
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
		return "", "", fmt.Errorf("no promoted-{commit}-{version} tag matches promoted-%s in %s", marker, stagingImage)
	}

	prefix := PromotedTagPrefix + commit + "-"
	for _, tag := range tags {
		if strings.HasPrefix(tag, prefix) && isPromotedVersionTag(tag, marker) {
			return parsePromotedTag(tag)
		}
	}
	return "", "", fmt.Errorf("no promoted tag found for commit %s in %s", commit, stagingImage)
}

func isPromotedVersionTag(tag, marker string) bool {
	return strings.HasPrefix(tag, PromotedTagPrefix) && tag != promotedTagFor(marker)
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
