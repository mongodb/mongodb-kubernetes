package release

import (
	"errors"
	"fmt"
	"strings"
)

const PromotedTagPrefix = "promoted-"

var ErrNotVersionedPromotedTag = errors.New("not a versioned promoted tag")

// ImageInfo is the raw information returned by the registry for a tag.
type ImageInfo struct {
	Tag    string
	Digest string
}

// Registry is a thin OCI registry client.
type Registry interface {
	ResolveByTag(tag string) (ImageInfo, error)
	FindTagsByDigest(digest string) ([]string, error)
}

// CommitChecker verifies whether a commit SHA exists in the git repository.
type CommitChecker interface {
	HasCommit(sha string) bool
}

// CandidateInfo holds the commit SHA and version for a promoted candidate.
type CandidateInfo struct {
	Commit  string
	Version string
}

// VerifyInputs are the parameters for a verify operation.
type VerifyInputs struct {
	Version string // required
	Commit  string // optional; empty means resolve from promoted-latest
}

// promotedLatestTag returns the rolling tag for the most recently promoted candidate.
func promotedLatestTag() string {
	return PromotedTagPrefix + "latest"
}

// PromotedTagFor constructs the immutable tag for a specific commit+version candidate.
func PromotedTagFor(commit, version string) string {
	return PromotedTagPrefix + commit + "-" + version
}

// ParsePromotedTag parses a "promoted-{commit}-{version}" tag into its components.
// Returns ErrNotVersionedPromotedTag for any tag that does not match this pattern.
func ParsePromotedTag(tag string) (commit, version string, err error) {
	rest, ok := strings.CutPrefix(tag, PromotedTagPrefix)
	if !ok {
		return "", "", ErrNotVersionedPromotedTag
	}
	commit, version, ok = strings.Cut(rest, "-")
	if !ok || commit == "" || version == "" || version[0] < '0' || version[0] > '9' {
		return "", "", ErrNotVersionedPromotedTag
	}
	return commit, version, nil
}

// Verify validates that a promoted candidate image matches the expected version.
//
// Latest mode (inputs.Commit empty): resolves promoted-latest, finds the versioned
// promoted tag sharing the same digest, and asserts the version matches inputs.Version.
//
// Explicit mode (inputs.Commit set): constructs the promoted-{commit}-{version} tag,
// verifies it exists in the registry, and checks the commit exists in git.
func Verify(inputs VerifyInputs, registry Registry, git CommitChecker) (CandidateInfo, error) {
	if inputs.Version == "" {
		return CandidateInfo{}, errors.New("version is required")
	}

	var candidate CandidateInfo

	if inputs.Commit == "" {
		tag := promotedLatestTag()
		info, err := registry.ResolveByTag(tag)
		if err != nil {
			return CandidateInfo{}, fmt.Errorf("resolve %s: %w", tag, err)
		}
		tags, err := registry.FindTagsByDigest(info.Digest)
		if err != nil {
			return CandidateInfo{}, fmt.Errorf("find versioned tag for %s: %w", tag, err)
		}
		candidate, err = versionedCandidateFrom(tags)
		if err != nil {
			return CandidateInfo{}, fmt.Errorf("no versioned promoted tag found for digest %s: %w", info.Digest, err)
		}
	} else {
		tag := PromotedTagFor(inputs.Commit, inputs.Version)
		if _, err := registry.ResolveByTag(tag); err != nil {
			return CandidateInfo{}, fmt.Errorf("promoted tag %s not found in registry: %w", tag, err)
		}
		candidate = CandidateInfo{Commit: inputs.Commit, Version: inputs.Version}
	}

	if !git.HasCommit(candidate.Commit) {
		return CandidateInfo{}, fmt.Errorf("commit %s from promoted tag not found in git repository", candidate.Commit)
	}

	if candidate.Version != inputs.Version {
		return CandidateInfo{}, fmt.Errorf("version mismatch: promoted tag has %s but expected %s", candidate.Version, inputs.Version)
	}

	return candidate, nil
}

// versionedCandidateFrom picks the first tag in the list that parses as promoted-{commit}-{version}.
func versionedCandidateFrom(tags []string) (CandidateInfo, error) {
	for _, t := range tags {
		commit, version, err := ParsePromotedTag(t)
		if err != nil {
			continue
		}
		return CandidateInfo{Commit: commit, Version: version}, nil
	}
	return CandidateInfo{}, errors.New("no versioned promoted tag in list")
}
