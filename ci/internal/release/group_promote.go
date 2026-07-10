package release

import (
	"errors"
	"fmt"
	"strings"
)

// GroupPromoteResult records what was (or would be) promoted for one image.
type GroupPromoteResult struct {
	Name     string   // build_info.json image key
	Repo     string   // primary staging repository (host/path) the tags were applied to
	Version  string   // release.json version the source image was tagged with
	Tags     []string // tags applied: promoted-{commit}-{version} and promoted-latest
	Warnings []string
}

// PromoteGroup promotes every image in the group at the given commit, writing
// promoted-{commit}-{version} and promoted-latest to each image's PRIMARY
// staging repository only (secondary repositories are intentionally left
// untouched for now). The source image is the version-tagged image the staging
// build already pushed, e.g. <staging-repo>:<version>.
//
// Stomping protection is applied group-wide, not per image: every image's
// immutable promoted-{commit}-{version} tag is checked for conflicts BEFORE
// any writes happen. If any image conflicts and force is false, the whole
// group is refused untouched — a partial promotion would leave the group in a
// mixed, inconsistent state. With force, every image proceeds.
//
// It hard-fails on the first image that cannot be promoted (e.g. its
// version-tagged source image is missing), so a broken merge build does not
// silently promote a partial group. connect resolves a Registry for an image's
// host; the CLI passes DefaultRegistryConnector and tests inject a fake.
func PromoteGroup(images []ReleaseImage, commit string, force, dryRun bool, connect RegistryConnector) ([]GroupPromoteResult, error) {
	if commit == "" {
		return nil, errors.New("commit is required")
	}
	if len(images) == 0 {
		return nil, errors.New("no images to promote")
	}

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
		srcRef := fmt.Sprintf("%s:%s", img.StagingRepo, img.Version)
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
		return nil, fmt.Errorf("refusing to promote group; tag conflicts found (use --force to override):\n%s", strings.Join(conflicts, "\n"))
	}

	results := make([]GroupPromoteResult, 0, len(images))
	for _, p := range prep {
		result, err := Promote(PromoteInputs{
			Image:   p.srcRef,
			Commit:  commit,
			Version: p.img.Version,
			Repo:    p.repoPath,
			// The group-wide check above already gated on conflicts; each
			// image proceeds regardless of its individual outcome.
			Force:  true,
			DryRun: dryRun,
		}, p.host, p.reg)
		if err != nil {
			return nil, fmt.Errorf("promote %s (%s): %w", p.img.Name, p.srcRef, err)
		}
		results = append(results, GroupPromoteResult{
			Name:     p.img.Name,
			Repo:     p.img.StagingRepo,
			Version:  p.img.Version,
			Tags:     result.Tags,
			Warnings: result.Warnings,
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
