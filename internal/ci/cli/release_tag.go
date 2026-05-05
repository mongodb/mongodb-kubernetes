package cli

import (
	"context"
	"fmt"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/release"
	"github.com/mongodb/mongodb-kubernetes/internal/ci/runner"
	"github.com/spf13/cobra"
)

func newReleaseTagCmd() *cobra.Command {
	var (
		version     string
		commit      string
		registryURL string
		repo        string
		dryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "tag",
		Short: "Tag a verified release candidate",
		Long: `Tag verifies a promoted candidate image and creates the release git tag.

Latest mode (--commit omitted): resolves promoted-latest, finds the versioned promoted
tag sharing the same digest, and asserts the version matches --version.

Explicit mode (--commit set): checks that promoted-{commit}-{version} exists in the
registry and that the commit is present in the local git repository.

With --dry-run the verification runs but no tag is created or pushed.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			git, err := release.NewGitChecker()
			if err != nil {
				return err
			}
			reg := release.NewOCIRegistry(registryURL, repo)
			candidate, err := release.Verify(
				release.VerifyInputs{Version: version, Commit: commit},
				reg, git,
			)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "verified: %s commit=%s version=%s\n",
				release.PromotedTagFor(candidate.Commit, candidate.Version),
				candidate.Commit,
				candidate.Version,
			)

			ctx := context.Background()
			r := runner.New(dryRun, "")
			gitTag := "v" + candidate.Version
			if err := r.Exec(ctx, "git", "tag", gitTag, candidate.Commit); err != nil {
				return fmt.Errorf("git tag: %w", err)
			}
			if err := r.Exec(ctx, "git", "push", "origin", gitTag); err != nil {
				return fmt.Errorf("git push tag: %w", err)
			}
			if !dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "tagged: %s → %s\n", candidate.Commit, gitTag)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "expected release version (required)")
	cmd.Flags().StringVar(&commit, "commit", "", "promoted commit SHA; omit to use promoted-latest")
	cmd.Flags().StringVar(&registryURL, "registry", "https://quay.io", "OCI registry base URL")
	cmd.Flags().StringVar(&repo, "repo", "mongodb/mongodb-kubernetes-operator", "image repository")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "verify the candidate without creating or pushing the tag")

	_ = cmd.MarkFlagRequired("version")
	_ = cmd.MarkFlagRequired("repo")

	return cmd
}
