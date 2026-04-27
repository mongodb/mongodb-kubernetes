package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/gitops"
	"github.com/mongodb/mongodb-kubernetes/internal/ci/release"
	"github.com/mongodb/mongodb-kubernetes/internal/ci/runner"
)

const releaseJSONPath = "release.json"

func newOpenReleasePRCmd() *cobra.Command {
	var (
		draft        bool
		dryRun       bool
		repoOverride string
	)
	cmd := &cobra.Command{
		Use:   "open-release-pr <version>",
		Short: "Bump release.json, regenerate artifacts, and open a release PR",
		Long: `Bumps release.json mongodbOperator to <version>, runs 'make precommit-full' to
regenerate the Helm chart, manifests, CSV, licenses and RBAC, commits the
result, re-runs precommit-full as an idempotency check, pushes the branch and
opens a PR.

By default the PR targets the same GitHub repo as origin (so a fork's origin
yields a fork-internal PR). Use --repo owner/repo to override.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOpenReleasePR(cmd.Context(), args[0], draft, dryRun, repoOverride)
		},
	}
	cmd.Flags().BoolVar(&draft, "draft", false, "open the PR as draft")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without executing them")
	cmd.Flags().StringVar(&repoOverride, "repo", "", "target repo for the PR (owner/repo); defaults to origin")
	return cmd
}

func runOpenReleasePR(ctx context.Context, version string, draft, dryRun bool, repoOverride string) error {
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

	if err := (release.PreflightInputs{
		Branch:        branch,
		WorktreeClean: clean,
		WantVersion:   version,
	}).Validate(); err != nil {
		return err
	}

	originRepo, err := gitops.DetectOriginRepo(ctx, r)
	if err != nil {
		return fmt.Errorf("detect origin repo (use --repo to override): %w", err)
	}
	headOwner := gitops.OwnerFromRepo(originRepo)
	if headOwner == "" {
		return fmt.Errorf("could not parse owner from origin repo %q", originRepo)
	}
	targetRepo := repoOverride
	if targetRepo == "" {
		targetRepo = originRepo
	}
	fmt.Fprintf(r.LogOut, "→ PR target: %s (head %s:%s)\n", targetRepo, headOwner, branch)

	if err := bumpReleaseJSON(r, version, dryRun); err != nil {
		return err
	}

	if err := r.Exec(ctx, "make", "precommit-full"); err != nil {
		return fmt.Errorf("make precommit-full: %w", err)
	}

	if err := commitReleaseChanges(ctx, r, version, dryRun); err != nil {
		return err
	}

	if err := r.Exec(ctx, "make", "precommit-full"); err != nil {
		return fmt.Errorf("make precommit-full (idempotency check): %w", err)
	}
	if !dryRun {
		clean, err := isWorktreeClean(ctx, r)
		if err != nil {
			return fmt.Errorf("check worktree after second precommit-full: %w", err)
		}
		if !clean {
			return fmt.Errorf("precommit-full produced changes on a second run; regeneration is not idempotent. Run `git status` and `git diff` to inspect")
		}
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
		"--repo", targetRepo,
		"--base", "master",
		"--head", headOwner + ":" + branch,
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

func bumpReleaseJSON(r *runner.Runner, version string, dryRun bool) error {
	if dryRun {
		fmt.Fprintf(r.LogOut, "[dry-run] would bump %s mongodbOperator to %s\n", releaseJSONPath, version)
		return nil
	}
	oldVersion, changed, err := release.BumpOperatorVersion(releaseJSONPath, version)
	if err != nil {
		return fmt.Errorf("bump %s: %w", releaseJSONPath, err)
	}
	if changed {
		fmt.Fprintf(r.LogOut, "→ bumped %s mongodbOperator: %s → %s\n", releaseJSONPath, oldVersion, version)
	} else {
		fmt.Fprintf(r.LogOut, "→ %s mongodbOperator already at %s; skipping write\n", releaseJSONPath, version)
	}
	return nil
}

func commitReleaseChanges(ctx context.Context, r *runner.Runner, version string, dryRun bool) error {
	if dryRun {
		fmt.Fprintln(r.LogOut, "[dry-run] would `git add -A` and commit if there are changes")
		return nil
	}
	clean, err := isWorktreeClean(ctx, r)
	if err != nil {
		return fmt.Errorf("check worktree post-precommit: %w", err)
	}
	if clean {
		fmt.Fprintln(r.LogOut, "→ no changes to commit (already at target?)")
		return nil
	}
	if err := r.Exec(ctx, "git", "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	msg := fmt.Sprintf("Release MCK %s", version)
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
