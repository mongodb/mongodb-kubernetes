package release

import (
	"os/exec"
	"strings"
	"testing"
)

// initRepo initialises a bare-minimum git repo in dir and returns the SHA of the initial commit.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestGitChecker_HasCommit(t *testing.T) {
	dir := t.TempDir()
	sha := initRepo(t, dir)

	checker := gitChecker{repoRoot: dir}

	if !checker.HasCommit(sha) {
		t.Errorf("expected HasCommit(%q) = true", sha)
	}
	if checker.HasCommit("0000000000000000000000000000000000000000") {
		t.Error("expected HasCommit(zeros) = false")
	}
}
