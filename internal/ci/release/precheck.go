package release

import (
	"errors"
	"fmt"
	"regexp"
)

// protectedReleaseBranchPattern matches branches in the GitHub-protected
// `release-*` namespace, which cannot be pushed to or merged from this tool.
var protectedReleaseBranchPattern = regexp.MustCompile(`^release-`)

// PreflightInputs are the conditions a release-PR command checks before
// taking any side-effecting action. They're collected by the orchestrator
// (current branch from git, worktree state from git, target version from
// the CLI argument) and validated here as a pure function.
type PreflightInputs struct {
	Branch        string
	WorktreeClean bool
	WantVersion   string
}

// Validate returns the first violation encountered, or nil if all checks pass.
// Errors are user-facing: they describe what went wrong and how to recover.
func (p PreflightInputs) Validate() error {
	if p.Branch == "" {
		return errors.New("could not determine current branch")
	}
	if p.Branch == "master" {
		return errors.New("cannot open a release PR from master; create a feature branch first")
	}
	if protectedReleaseBranchPattern.MatchString(p.Branch) {
		return fmt.Errorf("branch %q matches protected pattern 'release-*'; rename the branch before re-running", p.Branch)
	}
	if !p.WorktreeClean {
		return errors.New("working tree has uncommitted changes; commit or stash before running")
	}
	if p.WantVersion == "" {
		return errors.New("target version must not be empty")
	}
	return nil
}
