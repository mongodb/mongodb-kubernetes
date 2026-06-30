package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/ci/internal/release"
)

func newReleasePublishCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish promoted candidate(s) to the production registry",
	}

	cmd.AddCommand(newReleasePublishImageCmd())
	cmd.AddCommand(newReleasePublishGroupCmd())

	return cmd
}

func newReleasePublishImageCmd() *cobra.Command {
	var (
		stagingImage string
		commit       string
		registryURL  string
		prodRepo     string
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "image",
		Short: "Publish a single promoted candidate image",
		Long: `Resolves the promoted candidate for the given commit (or the latest promoted
commit if --commit is omitted), then retags it in the production registry as
:{version} and :latest. The version is derived from the promoted-{commit}-{version}
tag already present in the staging registry — no --version flag is needed.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := release.Publish(release.PublishInputs{
				StagingImage: stagingImage,
				Commit:       commit,
				ProdRepo:     prodRepo,
				DryRun:       dryRun,
			}, release.DefaultRegistryConnector(registryURL))
			if err != nil {
				return err
			}
			src := fmt.Sprintf("%s:%s", stagingImage, release.PromotedTagFor(result.Commit, result.Version))
			for _, tag := range result.AppliedTags {
				dst := fmt.Sprintf("%s/%s:%s", registryURL, prodRepo, tag)
				var line string
				if dryRun {
					line = fmt.Sprintf("dry-run: would copy %s → %s\n", src, dst)
				} else {
					line = fmt.Sprintf("published: %s → %s\n", src, dst)
				}
				if _, err := fmt.Fprint(cmd.OutOrStdout(), line); err != nil {
					return err
				}
			}
			return nil
		},
	}

	// Staging and production must share the --registry host; the host in
	// --staging-image is only used to derive the repo path.
	cmd.Flags().StringVar(&stagingImage, "staging-image", "", "staging image repo, e.g. quay.io/mongodb/staging/mongodb-kubernetes (required)")
	cmd.Flags().StringVar(&commit, "commit", "", "commit SHA to publish (default: latest promoted)")
	cmd.Flags().StringVar(&registryURL, "registry", "https://quay.io", "production OCI registry base URL")
	cmd.Flags().StringVar(&prodRepo, "prod-repo", "mongodb/mongodb-kubernetes-operator", "production image repository")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without copying any images")

	MustNotErr(cmd.MarkFlagRequired("staging-image"))

	return cmd
}

func newReleasePublishGroupCmd() *cobra.Command {
	var (
		buildInfo   string
		releaseJSON string
		commit      string
		dryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "group",
		Short: "Publish every promoted release image defined in build_info.json at the given commit",
		Long: `Resolves the release group from build_info.json and release.json, then for
each image retags its promoted-{commit}-{version} candidate (read from the
image's primary staging repository) as :{version} and :latest in the image's
production (release.repository) registry. --commit is required: a group
publish always publishes one specific, already-promoted commit consistently
across every image, rather than resolving promoted-latest independently per
image.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			images, err := release.LoadReleaseImages(buildInfo, releaseJSON)
			if err != nil {
				return err
			}
			results, err := release.PublishGroup(images, commit, dryRun, release.DefaultRegistryConnector)
			if err != nil {
				return err
			}
			for _, r := range results {
				src := fmt.Sprintf("%s:%s", r.StagingRepo, release.PromotedTagFor(r.Commit, r.Version))
				for _, tag := range r.Tags {
					dst := fmt.Sprintf("%s:%s", r.ProdRepo, tag)
					verb := "published"
					if dryRun {
						verb = "dry-run: would publish"
					}
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s → %s\n", verb, src, dst); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&buildInfo, "build-info", "build_info.json", "path to build_info.json")
	cmd.Flags().StringVar(&releaseJSON, "release-json", "release.json", "path to release.json")
	cmd.Flags().StringVar(&commit, "commit", "", "commit SHA to publish")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without copying any images")

	MustNotErr(cmd.MarkFlagRequired("commit"))

	return cmd
}
