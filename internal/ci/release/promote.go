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

func promotedLatestTag() string {
	return PromotedTagPrefix + "latest"
}

// PromoteInputs are the parameters for a promote operation.
type PromoteInputs struct {
	Image   string // source image reference (required)
	Commit  string // commit SHA for the promoted tag name (required)
	Version string // version for the promoted tag name (required)
	Repo    string // target repository under the registry host (required)
	DryRun  bool
}

func promote(inputs PromoteInputs, t Transport) ([]string, error) {
	if inputs.Image == "" {
		return nil, errors.New("image is required")
	}
	if inputs.Commit == "" {
		return nil, errors.New("commit is required")
	}
	if inputs.Version == "" {
		return nil, errors.New("version is required")
	}
	if inputs.Repo == "" {
		return nil, errors.New("repo is required")
	}

	tags := []string{
		PromotedTagFor(inputs.Commit, inputs.Version),
		promotedLatestTag(),
	}
	if inputs.DryRun {
		return tags, nil
	}
	if err := t.CopyWithTags(inputs.Image, inputs.Repo, tags); err != nil {
		return nil, fmt.Errorf("promote %s: %w", inputs.Image, err)
	}
	return tags, nil
}
