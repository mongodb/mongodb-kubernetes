package cli

import (
	"fmt"
	"log"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/ci/internal/release"
)

func newPromoteReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote candidate image(s) by applying promoted-latest and promoted-{commit}-{version} tags",
	}

	cmd.AddCommand(newPromoteImageCmd())
	cmd.AddCommand(newPromoteImagesCmd())

	return cmd
}

func newPromoteImageCmd() *cobra.Command {
	var (
		image        string
		commit       string
		version      string
		registryURL  string
		repo         string
		latestMarker string
		force        bool
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "image",
		Short: "Promote a single candidate image",
		Long: `Promotes a single candidate image, tagging it promoted-{commit}-{version}
and promoted-{latest-marker} (--latest-marker is required).

The promoted-{latest-marker} tag is a mutable pointer that always moves.
When promoting a backport (e.g. patching an older release branch after a
newer version has already been promoted), pass a distinct --latest-marker
(e.g. "latestv1") so the backport doesn't steal the "latest" pointer away
from the newest promoted version.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return promoteImage(cmd, image, commit, version, registryURL, repo, latestMarker, force, dryRun)
		},
	}

	cmd.Flags().StringVar(&image, "image", "", "source image reference to promote")
	cmd.Flags().StringVar(&commit, "commit", "", "commit SHA to encode in the promoted tag")
	cmd.Flags().StringVar(&version, "version", "", "version to encode in the promoted tag")
	cmd.Flags().StringVar(&registryURL, "registry", "https://quay.io", "target OCI registry base URL")
	cmd.Flags().StringVar(&repo, "repo", "mongodb/mongodb-kubernetes-operator", "target image repository")
	cmd.Flags().StringVar(&latestMarker, "latest-marker", "", "marker used for the mutable promoted-{marker} pointer tag (required; use a distinct value, e.g. \"latestv1\", for backports)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the promoted-{commit}-{version} tag even if it already points at a different digest")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without copying any images")

	MustNotErr(cmd.MarkFlagRequired("image"))
	MustNotErr(cmd.MarkFlagRequired("commit"))
	MustNotErr(cmd.MarkFlagRequired("version"))
	MustNotErr(cmd.MarkFlagRequired("latest-marker"))

	return cmd
}

func newPromoteImagesCmd() *cobra.Command {
	var (
		buildInfo    string
		releaseJSON  string
		commit       string
		latestMarker string
		force        bool
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "images",
		Short: "Promote every release image defined in build_info.json at the given commit",
		Long: `Promotes every release image in build_info.json at the given commit.

Every image's immutable promoted-{commit}-{version} tag is checked for
conflicts BEFORE any writes happen. If any image would overwrite an existing
tag with different content, the whole set is refused untouched — use
--force to promote anyway.

Every image also gets a promoted-{latest-marker} mutable pointer tag
(--latest-marker is required). When promoting a backport (e.g.
patching an older release branch after a newer version has already been
promoted), pass a distinct --latest-marker (e.g. "latestv1") so the backport
doesn't steal the "latest" pointer away from the newest promoted version.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return promoteAllImages(cmd, buildInfo, releaseJSON, commit, latestMarker, force, dryRun)
		},
	}

	cmd.Flags().StringVar(&buildInfo, "build-info", "build_info.json", "path to build_info.json")
	cmd.Flags().StringVar(&releaseJSON, "release-json", "release.json", "path to release.json")
	cmd.Flags().StringVar(&commit, "commit", "", "commit SHA to encode in the promoted tags")
	cmd.Flags().StringVar(&latestMarker, "latest-marker", "", "marker used for the mutable promoted-{marker} pointer tag (required; use a distinct value, e.g. \"latestv1\", for backports)")
	cmd.Flags().BoolVar(&force, "force", false, "promote every image even if any promoted tag already points at a different digest")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without copying any images")

	MustNotErr(cmd.MarkFlagRequired("commit"))
	MustNotErr(cmd.MarkFlagRequired("latest-marker"))

	return cmd
}

// promoteAllImages promotes every release image in build_info.json at the given
// commit, writing promoted tags to each image's primary staging repository.
func promoteAllImages(cmd *cobra.Command, buildInfo, releaseJSON, commit, latestMarker string, force, dryRun bool) error {
	images, err := release.LoadReleaseImages(buildInfo, releaseJSON)
	if err != nil {
		return err
	}
	results, err := release.PromoteImages(images, commit, latestMarker, force, dryRun, release.DefaultRegistryConnector)
	if err != nil {
		return err
	}
	for _, r := range results {
		if err := printNotices(cmd, r.Name, r.Infos, r.Warnings); err != nil {
			return err
		}
		for _, tag := range r.Tags {
			verb := "promoted"
			if dryRun {
				verb = "dry-run: would promote"
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s:%s\n", verb, r.Repo, tag); err != nil {
				return err
			}
		}
	}
	return nil
}

func promoteImage(cmd *cobra.Command, image, commit, version, registryURL, repo, latestMarker string, force, dryRun bool) error {
	host := strings.TrimPrefix(strings.TrimPrefix(registryURL, "https://"), "http://")
	result, err := release.Promote(release.PromoteInputs{
		Image:        image,
		Commit:       commit,
		Version:      version,
		Repo:         repo,
		LatestMarker: latestMarker,
		Force:        force,
		DryRun:       dryRun,
	}, host, release.DefaultRegistryConnector(registryURL))
	if err != nil {
		return err
	}
	if err := printNotices(cmd, "", result.Infos, result.Warnings); err != nil {
		return err
	}
	for _, tag := range result.Tags {
		var line string
		if dryRun {
			line = fmt.Sprintf("dry-run: would copy %s → %s/%s:%s\n", image, registryURL, repo, tag)
		} else {
			line = fmt.Sprintf("promoted: %s/%s:%s\n", registryURL, repo, tag)
		}
		if _, err := fmt.Fprint(cmd.OutOrStdout(), line); err != nil {
			return err
		}
	}
	return nil
}

// printNotices writes infos to stdout and warnings to stderr, each prefixed
// with name (if non-empty) to identify which image a result belongs to.
func printNotices(cmd *cobra.Command, name string, infos, warnings []string) error {
	prefix := ""
	if name != "" {
		prefix = name + ": "
	}
	for _, i := range infos {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "info: %s%s\n", prefix, i); err != nil {
			return err
		}
	}
	for _, w := range warnings {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s%s\n", prefix, w); err != nil {
			return err
		}
	}
	return nil
}

func MustNotErr(err error) {
	if err != nil {
		log.Fatalf("fatal error: %v", err)
	}
}
