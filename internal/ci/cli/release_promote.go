package cli

import (
	"fmt"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/release"
	"github.com/spf13/cobra"
)

func newReleasePromoteCmd() *cobra.Command {
	var (
		image       string
		commit      string
		version     string
		registryURL string
		repo        string
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
			}, promoter)
			if err != nil {
				return err
			}
			for _, tag := range tags {
				fmt.Fprintf(cmd.OutOrStdout(), "promoted: %s/%s:%s\n", registryURL, repo, tag)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&image, "image", "", "source image reference to promote (required)")
	cmd.Flags().StringVar(&commit, "commit", "", "commit SHA to encode in the promoted tag (required)")
	cmd.Flags().StringVar(&version, "version", "", "version to encode in the promoted tag (required)")
	cmd.Flags().StringVar(&registryURL, "registry", "https://quay.io", "target OCI registry base URL")
	cmd.Flags().StringVar(&repo, "repo", "mongodb/mongodb-kubernetes-operator", "target image repository")

	_ = cmd.MarkFlagRequired("image")
	_ = cmd.MarkFlagRequired("commit")
	_ = cmd.MarkFlagRequired("version")

	return cmd
}
