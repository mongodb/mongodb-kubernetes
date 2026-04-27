package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/release"
	"github.com/mongodb/mongodb-kubernetes/internal/ci/runner"
)

func newOpenReleasePRCmd() *cobra.Command {
	var (
		draft  bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "open-release-pr <version>",
		Short: "Open a release PR for the given MCK version",
		Long: `Validates the current branch + worktree, regenerates release artifacts via
'make precommit-full', commits any changes, pushes the branch, and opens a PR
against master.

Intended to be run after release.json has been bumped and copy_release_dockerfiles
has been run on a feature branch.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOpenReleasePR(cmd.Context(), args[0], draft, dryRun)
		},
	}
	cmd.Flags().BoolVar(&draft, "draft", false, "open the PR as draft")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without executing them")
	return cmd
}

func runOpenReleasePR(ctx context.Context, version string, draft, dryRun bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	r := runner.New(dryRun, cwd)

	branch, err := r.Capture(ctx, "git", "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("get current branch: %w", err)
	}

	clean, err := isWorktreeClean(ctx, r)
	if err != nil {
		return fmt.Errorf("check worktree: %w", err)
	}

	releaseVer, err := release.ReadOperatorVersion("release.json")
	if err != nil {
		return err
	}

	if err := (release.PreflightInputs{
		Branch:         branch,
		WorktreeClean:  clean,
		ReleaseJSONVer: releaseVer,
		WantVersion:    version,
	}).Validate(); err != nil {
		return err
	}

	if err := r.Exec(ctx, "make", "precommit-full"); err != nil {
		return fmt.Errorf("make precommit-full: %w", err)
	}

	if err := commitRegeneratedArtifacts(ctx, r, version, dryRun); err != nil {
		return err
	}

	if err := r.Exec(ctx, "git", "push", "-u", "origin", branch); err != nil {
		return fmt.Errorf("git push: %w", err)
	}

	body, err := release.RenderPRBody(version)
	if err != nil {
		return err
	}
	ghArgs := []string{
		"pr", "create",
		"--base", "master",
		"--head", branch,
		"--title", release.PRTitle(version),
		"--body", body,
	}
	if draft {
		ghArgs = append(ghArgs, "--draft")
	}
	if err := r.Exec(ctx, "gh", ghArgs...); err != nil {
		return fmt.Errorf("gh pr create: %w", err)
	}
	return nil
}

func commitRegeneratedArtifacts(ctx context.Context, r *runner.Runner, version string, dryRun bool) error {
	if dryRun {
		fmt.Fprintln(r.LogOut, "[dry-run] would `git add -A` and commit if precommit-full produced changes")
		return nil
	}
	clean, err := isWorktreeClean(ctx, r)
	if err != nil {
		return fmt.Errorf("check worktree post-precommit: %w", err)
	}
	if clean {
		fmt.Fprintln(r.LogOut, "→ no files changed by precommit-full; skipping commit")
		return nil
	}
	if err := r.Exec(ctx, "git", "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	msg := fmt.Sprintf("Regenerate release artifacts for %s", version)
	if err := r.Exec(ctx, "git", "commit", "-m", msg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// isWorktreeClean returns true if both the working tree and the index are
// clean. `git diff --quiet` exits 0 when clean, 1 when dirty; any other
// non-zero is a real error.
func isWorktreeClean(ctx context.Context, r *runner.Runner) (bool, error) {
	for _, args := range [][]string{
		{"diff", "--quiet"},
		{"diff", "--cached", "--quiet"},
	} {
		err := r.CheckExitCode(ctx, "git", args...)
		switch runner.ExitCode(err) {
		case 0:
			continue
		case 1:
			return false, nil
		default:
			return false, err
		}
	}
	return true, nil
}
