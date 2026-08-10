package release

import (
	"errors"
	"fmt"
)

const PromotedTagPrefix = "promoted-"

// PromotedTagFor constructs the immutable tag for a specific commit+version candidate.
func PromotedTagFor(commit, version string) string {
	return PromotedTagPrefix + commit + "-" + version
}

// promotedTagFor builds the mutable promoted-{marker} pointer tag, e.g.
// "promoted-latest" or "promoted-latestv1" for a backport marker.
func promotedTagFor(marker string) string {
	return PromotedTagPrefix + marker
}

func promotedLatestTag() string {
	return promotedTagFor("latest")
}

// PromoteInputs are the parameters for a promote operation.
type PromoteInputs struct {
	Image   string // source image reference (required)
	Commit  string // commit SHA for the promoted tag name (required)
	Version string // version for the promoted tag name (required)
	Repo    string // target repository under the registry host (required)
	// LatestMarker customizes the mutable promoted-{marker} pointer tag that
	// is moved alongside the immutable promoted-{commit}-{version} tag
	// (required; the CLI defaults it to "latest" via the --latest-marker
	// flag). Backport releases (e.g. patching an older branch after a newer
	// version has already been released) should pass a distinct marker
	// (e.g. "latestv1") so they don't steal the "latest" pointer away from
	// the newest release.
	LatestMarker string
	// Force allows overwriting the immutable promoted-{commit}-{version} tag
	// even if it already points at a different digest. Without it, such a
	// conflict is a hard error (image stomping protection).
	Force  bool
	DryRun bool
}

// PromoteResult holds the tags applied (or that would be applied), any
// non-fatal warnings surfaced along the way, and informational notices.
type PromoteResult struct {
	Tags     []string
	Warnings []string
	Infos    []string
}

// Promote copies the source image to promoted-{commit}-{version} and
// promoted-latest in the target repo, using reg to talk to the registry.
// host is the destination registry host (e.g. "quay.io"); inputs.Repo is a
// path relative to it, matching Registry.CopyWithTags' own convention. It is
// needed to build a fully-qualified destination ref for the stomp check,
// since Registry.Digest (like ListTags) never prefixes a host itself.
func Promote(inputs PromoteInputs, host string, reg Registry) (PromoteResult, error) {
	if inputs.Image == "" {
		return PromoteResult{}, errors.New("image is required")
	}
	if inputs.Commit == "" {
		return PromoteResult{}, errors.New("commit is required")
	}
	if inputs.Version == "" {
		return PromoteResult{}, errors.New("version is required")
	}
	if inputs.Repo == "" {
		return PromoteResult{}, errors.New("repo is required")
	}
	if inputs.LatestMarker == "" {
		return PromoteResult{}, errors.New("latest-marker is required")
	}

	versionTag := PromotedTagFor(inputs.Commit, inputs.Version)
	latestTag := promotedTagFor(inputs.LatestMarker)
	dstVersionRef := fmt.Sprintf("%s/%s:%s", host, inputs.Repo, versionTag)

	// Only the immutable per-version tag is stomp-checked; promoted-latest is
	// a mutable pointer that is meant to move on every promotion.
	conflict, upToDate, err := checkStomp(reg, inputs.Image, dstVersionRef)
	if err != nil {
		return PromoteResult{}, err
	}
	if conflict != "" && !inputs.Force {
		return PromoteResult{}, fmt.Errorf("refusing to promote: %s (use --force is the tag needs overwriting)", conflict)
	}

	var warnings, infos []string
	if upToDate {
		infos = append(infos, fmt.Sprintf("%s is already in place", dstVersionRef))
	} else if conflict != "" {
		warnings = append(warnings, fmt.Sprintf("overwriting due to --force: %s", conflict))
	}

	result := PromoteResult{Tags: []string{versionTag, latestTag}, Warnings: warnings, Infos: infos}
	if inputs.DryRun {
		return result, nil
	}

	// Immutable tag first: only once that succeeds do we move the mutable
	// latest pointer, so a failure never leaves latest ahead of the
	// version-tagged image it's supposed to point at.
	if err := reg.CopyWithTags(inputs.Image, inputs.Repo, []string{versionTag}); err != nil {
		return PromoteResult{}, fmt.Errorf("promote %s: %w", inputs.Image, err)
	}
	if err := reg.CopyWithTags(inputs.Image, inputs.Repo, []string{latestTag}); err != nil {
		return PromoteResult{}, fmt.Errorf("promote %s (latest): %w", inputs.Image, err)
	}
	return result, nil
}
