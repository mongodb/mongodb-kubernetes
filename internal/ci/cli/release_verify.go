package cli

import (
	"fmt"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/release"
	"github.com/spf13/cobra"
)

func newReleaseVerifyCmd() *cobra.Command {
	var (
		version     string
		commit      string
		registryURL string
		repo        string
	)

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify a promoted candidate image is ready to release",
		Long: `Verify checks that a promoted image exists in the registry and its commit is present in git.

Latest mode (--commit omitted): resolves promoted-latest, finds the versioned promoted
tag sharing the same digest, and asserts the version matches --version.

Explicit mode (--commit set): checks that promoted-{commit}-{version} exists in the
registry and that the commit is present in the local git repository.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			git, err := release.NewGitChecker()
			if err != nil {
				return err
			}
			reg := release.NewOCIRegistry(registryURL, repo)
			candidate, err := release.Verify(release.VerifyInputs{Version: version, Commit: commit}, reg, git)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "verified: %s commit=%s version=%s\n",
				release.PromotedTagFor(candidate.Commit, candidate.Version),
				candidate.Commit,
				candidate.Version,
			)
			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "expected release version (required)")
	cmd.Flags().StringVar(&commit, "commit", "", "promoted commit SHA; omit to use promoted-latest")
	cmd.Flags().StringVar(&registryURL, "registry", "https://quay.io", "OCI registry base URL")
	cmd.Flags().StringVar(&repo, "repo", "mongodb/mongodb-kubernetes-operator", "image repository, e.g. mongodb/mongodb-kubernetes-operator (required)")

	_ = cmd.MarkFlagRequired("version")
	_ = cmd.MarkFlagRequired("repo")

	return cmd
}
