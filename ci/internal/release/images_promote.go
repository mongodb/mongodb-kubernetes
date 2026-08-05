package release

import (
	"errors"
	"fmt"
	"strings"
)

// ImagesPromoteResult records what was (or would be) promoted for one image.
type ImagesPromoteResult struct {
	Name     string
	Repo     string
	Version  string
	Tags     []string
	Warnings []string
	Infos    []string
}

// shortCommit mirrors the COMMIT_SHA_SHORT computed in
// scripts/dev/contexts/evg-private-context (via git rev-parse --short=8 HEAD)
// and used as the staging build tag in scripts/release/atomic_pipeline.py.
// If commit is longer than 8 characters the first 8 are returned; otherwise
// commit is returned unchanged (handles both full 40-char SHAs and values
// already shortened).
func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

// PromoteImages promotes every image at the given commit, writing
// promoted-{commit}-{version} and promoted-{latestMarker} to each image's PRIMARY
// staging repository only (secondary repositories are intentionally left
// untouched for now). The source image is the short-commit-tagged image the
// staging build already pushed, e.g. <staging-repo>:<short-commit> (matching the
// COMMIT_SHA_SHORT tag produced by scripts/dev/contexts/evg-private-context and
// scripts/release/atomic_pipeline.py).
//
// It hard-fails on the first image that cannot be promoted (e.g. its
// commit-tagged source image is missing), so a broken merge build does not
// silently promote a partial image set. connect resolves a Registry for an image's
// host; the CLI passes DefaultRegistryConnector and tests inject a fake.
func PromoteImages(images []ReleaseImage, commit, latestMarker string, force, dryRun bool, connect RegistryConnector) ([]ImagesPromoteResult, error) {
	if commit == "" {
		return nil, errors.New("commit is required")
	}
	if len(images) == 0 {
		return nil, errors.New("no images to promote")
	}
	if latestMarker == "" {
		return nil, errors.New("latest-marker is required")
	}
	srcTag := shortCommit(commit)

	type prepared struct {
		img        ReleaseImage
		reg        Registry
		host       string
		repoPath   string
		srcRef     string
		versionTag string
	}

	prep := make([]prepared, 0, len(images))
	var conflicts []string
	for _, img := range images {
		host, path := splitHostRepo(img.StagingRepo)
		reg := connect(host)
		srcRef := fmt.Sprintf("%s:%s", img.StagingRepo, srcTag)
		versionTag := PromotedTagFor(commit, img.Version)

		dstVersionRef := fmt.Sprintf("%s/%s:%s", host, path, versionTag)
		conflict, _, err := checkStomp(reg, srcRef, dstVersionRef)
		if err != nil {
			return nil, fmt.Errorf("check %s (%s): %w", img.Name, srcRef, err)
		}
		if conflict != "" {
			conflicts = append(conflicts, fmt.Sprintf("%s: %s", img.Name, conflict))
		}
		prep = append(prep, prepared{img: img, reg: reg, host: host, repoPath: path, srcRef: srcRef, versionTag: versionTag})
	}
	if len(conflicts) > 0 && !force {
		return nil, fmt.Errorf("refusing to promote images; tag conflicts found (use --force if the tags needs overwriting):\n%s", strings.Join(conflicts, "\n"))
	}

	results := make([]ImagesPromoteResult, 0, len(images))
	for _, p := range prep {
		result, err := Promote(PromoteInputs{
			Image:        p.srcRef,
			Commit:       commit,
			Version:      p.img.Version,
			Repo:         p.repoPath,
			LatestMarker: latestMarker,
			Force:        true,
			DryRun:       dryRun,
		}, p.host, p.reg)
		if err != nil {
			return nil, fmt.Errorf("promote %s (%s): %w", p.img.Name, p.srcRef, err)
		}
		results = append(results, ImagesPromoteResult{
			Name:     p.img.Name,
			Repo:     p.img.StagingRepo,
			Version:  p.img.Version,
			Tags:     result.Tags,
			Warnings: result.Warnings,
			Infos:    result.Infos,
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
