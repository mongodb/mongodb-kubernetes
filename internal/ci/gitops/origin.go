// Package gitops contains git helpers used across mck-ci subcommands.
// Currently scoped to remote-URL handling; the package will grow as more
// orchestration moves out of the cli package.
package gitops

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/runner"
)

var gitHubURLPatterns = []*regexp.Regexp{
	// git@github.com:owner/repo[.git]
	regexp.MustCompile(`^git@github\.com:([^/]+)/([^/]+?)(?:\.git)?/?$`),
	// https://github.com/owner/repo[.git]
	regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+?)(?:\.git)?/?$`),
	// ssh://git@github.com/owner/repo[.git]
	regexp.MustCompile(`^ssh://git@github\.com/([^/]+)/([^/]+?)(?:\.git)?/?$`),
}

// ParseGitHubRepo extracts "owner/repo" from a GitHub remote URL. Returns an
// error if the URL is not a recognized GitHub form (e.g. GitLab URLs, malformed
// strings, or empty input).
func ParseGitHubRepo(remoteURL string) (string, error) {
	s := strings.TrimSpace(remoteURL)
	for _, re := range gitHubURLPatterns {
		if m := re.FindStringSubmatch(s); m != nil {
			return m[1] + "/" + m[2], nil
		}
	}
	return "", fmt.Errorf("not a recognized GitHub URL: %q", remoteURL)
}

// OwnerFromRepo returns the owner part of an "owner/repo" string. Returns an
// empty string if the input is not in that form.
func OwnerFromRepo(ownerRepo string) string {
	if i := strings.IndexByte(ownerRepo, '/'); i > 0 {
		return ownerRepo[:i]
	}
	return ""
}

// DetectOriginRepo returns the "owner/repo" pointed at by `git remote get-url
// origin`. The runner is used so dry-run logging and working-directory
// configuration stay consistent with the rest of the orchestrator.
func DetectOriginRepo(ctx context.Context, r *runner.Runner) (string, error) {
	out, err := r.Capture(ctx, "git", "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return ParseGitHubRepo(out)
}
