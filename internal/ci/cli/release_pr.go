package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/release"
	"github.com/spf13/cobra"
)

func newReleasePRCmd() *cobra.Command {
	var version string

	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Open a release PR that appends the version to release.json supported image lists",
		RunE: func(cmd *cobra.Command, _ []string) error {
			prURL, err := release.ReleasePR(
				release.PRInputs{Version: version},
				&ghPROpener{},
			)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), prURL)
			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "release version to cut (required)")
	_ = cmd.MarkFlagRequired("version")

	return cmd
}

// ghPROpener implements PROpener: branches, commits, pushes, then opens the PR via gh.
type ghPROpener struct{}

func (g *ghPROpener) Open(repoRoot, branch, title, body string) (string, error) {
	for _, args := range [][]string{
		{"checkout", "-b", branch},
		{"add", "release.json"},
		{"commit", "-m", title},
		{"push", "-u", "origin", branch},
	} {
		cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %v: %w\n%s", args, err, out)
		}
	}

	repoOut, err := exec.Command("gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner").Output()
	if err != nil {
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	repo := strings.TrimSpace(string(repoOut))

	out, err := exec.Command("gh", "pr", "create",
		"--repo", repo,
		"--title", title,
		"--body", body,
		"--label", "skip-changelog",
	).Output()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
