package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/ci/internal/release"
)

func newPublishReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish promoted candidate(s) to the production registry",
	}

	cmd.AddCommand(newPublishImageCmd())
	cmd.AddCommand(newPublishImagesCmd())

	return cmd
}

func newPublishImageCmd() *cobra.Command {
	var (
		stagingImage string
		commit       string
		registryURL  string
		prodRepo     string
		latestMarker string
		force        bool
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "image",
		Short: "Publish a single promoted candidate image",
		Long: `Resolves the promoted candidate for the given commit (or the promoted
candidate matching promoted-{latest-marker} in staging if --commit is
omitted), then retags it in the production registry as :{version} and
:{latest-marker}. The version is derived from the promoted-{commit}-{version}
tag already present in the staging registry — no --version flag is needed.

The immutable :{version} tag is checked for conflicts before it is
overwritten: if it already exists at a different digest, the publish is
refused unless --force is given. :{latest-marker} always moves.

--latest-marker is required. When publishing a backport (e.g.
patching an older release branch after a newer version has already been
published), pass a distinct --latest-marker (e.g. "latestv1") so the
backport doesn't steal the ":latest" tag away from the newest published
version.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			host := strings.TrimPrefix(strings.TrimPrefix(registryURL, "https://"), "http://")
			result, err := release.Publish(release.PublishInputs{
				StagingImage: stagingImage,
				Commit:       commit,
				ProdRepo:     prodRepo,
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
	cmd.Flags().StringVar(&latestMarker, "latest-marker", "", "marker used for the mutable :{marker} production tag (required; use a distinct value, e.g. \"latestv1\", for backports)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the :{version} production tag even if it already points at a different digest")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without copying any images")

	MustNotErr(cmd.MarkFlagRequired("staging-image"))
	MustNotErr(cmd.MarkFlagRequired("latest-marker"))

	return cmd
}

func newPublishImagesCmd() *cobra.Command {
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
		Short: "Publish every promoted release image defined in build_info.json at the given commit",
		Long: `Resolves the release image set from build_info.json and release.json, then
for each image retags its promoted-{commit}-{version} candidate (read from
the image's primary staging repository) as :{version} and :{latest-marker}
in the image's production (release.repository) registry. --commit is
required: a multi-image publish always publishes one specific,
already-promoted commit consistently across every image, rather than
resolving promoted-{latest-marker} independently per image.

Every image's immutable :{version} production tag is checked for conflicts
BEFORE any writes happen. If any image would overwrite an existing tag with
different content, the whole set is refused untouched — use --force to
publish anyway. :{latest-marker} always moves.

--latest-marker is required. When publishing a backport across
every image (e.g. patching an older release branch after a newer version has
already been published), pass a distinct --latest-marker (e.g. "latestv1")
so the backport doesn't steal the ":latest" tag away from the newest
published version.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			images, err := release.LoadReleaseImages(buildInfo, releaseJSON)
			if err != nil {
				return err
			}
			results, err := release.PublishImages(images, commit, latestMarker, force, dryRun, release.DefaultRegistryConnector)
			if err != nil {
				return err
			}
			for _, r := range results {
				if err := printNotices(cmd, r.Name, r.Infos, r.Warnings); err != nil {
					return err
				}
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
	cmd.Flags().StringVar(&latestMarker, "latest-marker", "", "marker used for the mutable :{marker} production tag (required; use a distinct value, e.g. \"latestv1\", for backports)")
	cmd.Flags().BoolVar(&force, "force", false, "publish every image even if any production tag already points at a different digest")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without copying any images")

	MustNotErr(cmd.MarkFlagRequired("commit"))
	MustNotErr(cmd.MarkFlagRequired("latest-marker"))

	return cmd
}
