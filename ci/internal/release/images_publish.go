package release

import (
	"errors"
	"fmt"
	"strings"
)

type ImagesPublishResult struct {
	Name        string
	StagingRepo string
	ProdRepo    string
	Commit      string
	Version     string
	Tags        []string
	Warnings    []string
	Infos       []string
}

func PublishImages(images []ReleaseImage, commit, latestMarker string, force, dryRun bool, connect RegistryConnector) ([]ImagesPublishResult, error) {
	if commit == "" {
		return nil, errors.New("commit is required")
	}
	if len(images) == 0 {
		return nil, errors.New("no images to publish")
	}
	if latestMarker == "" {
		return nil, errors.New("latest-marker is required")
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

		_, version, err := resolvePromoted(img.StagingRepo, commit, latestMarker, reg)
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
		return nil, fmt.Errorf("refusing to publish images; tag conflicts found (use --force to override):\n%s", strings.Join(conflicts, "\n"))
	}

	results := make([]ImagesPublishResult, 0, len(images))
	for _, p := range prep {
		result, err := Publish(PublishInputs{
			StagingImage: p.img.StagingRepo,
			Commit:       commit,
			ProdRepo:     p.prodPath,
			LatestMarker: latestMarker,
			Force:        true,
			DryRun:       dryRun,
		}, p.host, p.reg)
		if err != nil {
			return nil, fmt.Errorf("publish %s (%s): %w", p.img.Name, p.img.StagingRepo, err)
		}
		results = append(results, ImagesPublishResult{
			Name:        p.img.Name,
			StagingRepo: p.img.StagingRepo,
			ProdRepo:    p.img.ReleaseRepo,
			Commit:      result.Commit,
			Version:     result.Version,
			Tags:        result.AppliedTags,
			Warnings:    result.Warnings,
			Infos:       result.Infos,
		})
	}
	return results, nil
}
