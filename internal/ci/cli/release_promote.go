package cli

import (
	"fmt"
	"log"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/release"
)

func newReleasePromoteCmd() *cobra.Command {
	var (
		image       string
		commit      string
		version     string
		registryURL string
		repo        string
		dryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote a candidate image by applying promoted-latest and promoted-{commit}-{version} tags",
		RunE: func(cmd *cobra.Command, _ []string) error {
			promoter := release.NewOCIPromoter(registryURL, repo)
			tags, err := release.Promote(release.PromoteInputs{
				Image:   image,
				Commit:  commit,
				Version: version,
				DryRun:  dryRun,
			}, promoter)
			if err != nil {
				return err
			}
			for _, tag := range tags {
				if dryRun {
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would copy %s → %s/%s:%s\n", image, registryURL, repo, tag)
				} else {
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "promoted: %s/%s:%s\n", registryURL, repo, tag)
				}
				if err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&image, "image", "", "source image reference to promote (required)")
	cmd.Flags().StringVar(&commit, "commit", "", "commit SHA to encode in the promoted tag (required)")
	cmd.Flags().StringVar(&version, "version", "", "version to encode in the promoted tag (required)")
	cmd.Flags().StringVar(&registryURL, "registry", "https://quay.io", "target OCI registry base URL")
	cmd.Flags().StringVar(&repo, "repo", "mongodb/mongodb-kubernetes-operator", "target image repository")

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without copying any images")

	MustNotErr(cmd.MarkFlagRequired("image"))
	MustNotErr(cmd.MarkFlagRequired("commit"))
	MustNotErr(cmd.MarkFlagRequired("version"))

	return cmd
}

func MustNotErr(err error) {
	if err != nil {
		log.Fatalf("fatal error: %v", err)
	}
}
