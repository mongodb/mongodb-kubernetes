package cli

import (
	"fmt"
	"log"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/ci/internal/release"
)

func newReleasePromoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote candidate image(s) by applying promoted-latest and promoted-{commit}-{version} tags",
	}

	cmd.AddCommand(newReleasePromoteImageCmd())
	cmd.AddCommand(newReleasePromoteGroupCmd())

	return cmd
}

func newReleasePromoteImageCmd() *cobra.Command {
	var (
		image       string
		commit      string
		version     string
		registryURL string
		repo        string
		force       bool
		dryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "image",
		Short: "Promote a single candidate image",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSinglePromote(cmd, image, commit, version, registryURL, repo, force, dryRun)
		},
	}

	cmd.Flags().StringVar(&image, "image", "", "source image reference to promote")
	cmd.Flags().StringVar(&commit, "commit", "", "commit SHA to encode in the promoted tag")
	cmd.Flags().StringVar(&version, "version", "", "version to encode in the promoted tag")
	cmd.Flags().StringVar(&registryURL, "registry", "https://quay.io", "target OCI registry base URL")
	cmd.Flags().StringVar(&repo, "repo", "mongodb/mongodb-kubernetes-operator", "target image repository")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the promoted-{commit}-{version} tag even if it already points at a different digest")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without copying any images")

	MustNotErr(cmd.MarkFlagRequired("image"))
	MustNotErr(cmd.MarkFlagRequired("commit"))
	MustNotErr(cmd.MarkFlagRequired("version"))

	return cmd
}

func newReleasePromoteGroupCmd() *cobra.Command {
	var (
		buildInfo   string
		releaseJSON string
		commit      string
		force       bool
		dryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "group",
		Short: "Promote every release image defined in build_info.json at the given commit",
		Long: `Promotes every release image in build_info.json at the given commit.

Every image's immutable promoted-{commit}-{version} tag is checked for
conflicts BEFORE any writes happen. If any image would overwrite an existing
tag with different content, the whole group is refused untouched — use
--force to promote anyway.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGroupPromote(cmd, buildInfo, releaseJSON, commit, force, dryRun)
		},
	}

	cmd.Flags().StringVar(&buildInfo, "build-info", "build_info.json", "path to build_info.json")
	cmd.Flags().StringVar(&releaseJSON, "release-json", "release.json", "path to release.json")
	cmd.Flags().StringVar(&commit, "commit", "", "commit SHA to encode in the promoted tags")
	cmd.Flags().BoolVar(&force, "force", false, "promote the whole group even if any image's promoted tag already points at a different digest")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without copying any images")

	MustNotErr(cmd.MarkFlagRequired("commit"))

	return cmd
}

// runGroupPromote promotes every release image in build_info.json at the given
// commit, writing promoted tags to each image's primary staging repository.
func runGroupPromote(cmd *cobra.Command, buildInfo, releaseJSON, commit string, force, dryRun bool) error {
	images, err := release.LoadReleaseImages(buildInfo, releaseJSON)
	if err != nil {
		return err
	}
	results, err := release.PromoteGroup(images, commit, force, dryRun, release.DefaultRegistryConnector)
	if err != nil {
		return err
	}
	for _, r := range results {
		for _, w := range r.Warnings {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %s\n", r.Name, w); err != nil {
				return err
			}
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

func runSinglePromote(cmd *cobra.Command, image, commit, version, registryURL, repo string, force, dryRun bool) error {
	host := strings.TrimPrefix(strings.TrimPrefix(registryURL, "https://"), "http://")
	result, err := release.Promote(release.PromoteInputs{
		Image:   image,
		Commit:  commit,
		Version: version,
		Repo:    repo,
		Force:   force,
		DryRun:  dryRun,
	}, host, release.DefaultRegistryConnector(registryURL))
	if err != nil {
		return err
	}
	for _, w := range result.Warnings {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w); err != nil {
			return err
		}
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

func MustNotErr(err error) {
	if err != nil {
		log.Fatalf("fatal error: %v", err)
	}
}
