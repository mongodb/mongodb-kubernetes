package release

import (
	"fmt"
	"os/exec"
	"strings"
)

type gitChecker struct {
	repoRoot string
}

// NewGitChecker discovers the git repo root from the current working directory.
func NewGitChecker() (CommitChecker, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, fmt.Errorf("not in a git repository: %w", err)
	}
	return &gitChecker{repoRoot: strings.TrimSpace(string(out))}, nil
}

func (g *gitChecker) HasCommit(sha string) bool {
	err := exec.Command("git", "-C", g.repoRoot, "cat-file", "-e", sha+"^{commit}").Run()
	return err == nil
}
