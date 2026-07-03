package evergreen_test

import (
	"os"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestE2EFilePattern(t *testing.T) {
	re := loadE2EFilePattern(t)

	tests := []struct {
		desc        string
		files       []string
		shouldMatch bool
	}{
		// --- should trigger e2e ---
		{
			desc:        "root-level Go file",
			files:       []string{"main.go"},
			shouldMatch: true,
		},
		{
			desc:        "root-level Python file",
			files:       []string{"generate_ssdlc_report_test.py"},
			shouldMatch: true,
		},
		{
			desc:        "api package",
			files:       []string{"api/v1/types.go"},
			shouldMatch: true,
		},
		{
			desc:        "controllers package",
			files:       []string{"controllers/operator/foo.go"},
			shouldMatch: true,
		},
		{
			desc:        "internal package",
			files:       []string{"internal/util/foo.go"},
			shouldMatch: true,
		},
		{
			desc:        "pkg package",
			files:       []string{"pkg/kube/client.go"},
			shouldMatch: true,
		},
		{
			desc:        "cmd package",
			files:       []string{"cmd/manager/main.go"},
			shouldMatch: true,
		},
		{
			desc:        "docker package",
			files:       []string{"docker/agent/main.go"},
			shouldMatch: true,
		},
		{
			desc:        "scripts Go file",
			files:       []string{"scripts/dev/reset/main.go"},
			shouldMatch: true,
		},
		{
			desc:        "scripts Python file",
			files:       []string{"scripts/release/version.py"},
			shouldMatch: true,
		},
		{
			desc:        "docker Python test",
			files:       []string{"docker/mongodb-kubernetes-tests/kubetester/test_identifiers.py"},
			shouldMatch: true,
		},
		{
			desc:        "mongodb-community-operator package",
			files:       []string{"mongodb-community-operator/pkg/foo.go"},
			shouldMatch: true,
		},
		{
			desc:        "mix of prod and non-prod files",
			files:       []string{".evergreen.yml", "api/v1/types.go"},
			shouldMatch: true,
		},
		// --- should NOT trigger e2e ---
		{
			desc:        "evergreen config only",
			files:       []string{".evergreen.yml"},
			shouldMatch: false,
		},
		{
			desc:        "ci tooling Go files",
			files:       []string{"ci/cmd/mckci/main.go", "ci/internal/cli/root.go"},
			shouldMatch: false,
		},
		{
			desc:        "Makefile only",
			files:       []string{"Makefile"},
			shouldMatch: false,
		},
		{
			desc:        "scripts non-code files only",
			files:       []string{"scripts/mckci", "scripts/code_snippets/archive.sh"},
			shouldMatch: false,
		},
		{
			desc:        "docs and yaml only",
			files:       []string{"README.md", "helm_chart/values.yaml", "release.json"},
			shouldMatch: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			matched := false
			for _, f := range tc.files {
				if re.MatchString(f) {
					matched = true
					break
				}
			}
			if matched != tc.shouldMatch {
				if tc.shouldMatch {
					t.Errorf("expected at least one of %v to match %s, but none did", tc.files, re)
				} else {
					t.Errorf("expected none of %v to match %s, but at least one did", tc.files, re)
				}
			}
		})
	}
}

// loadE2EFilePattern reads the file_pattern from the pr_patch_e2e entry in
// github_pr_aliases so the test stays in sync with the real config.
func loadE2EFilePattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	data, err := os.ReadFile("../../../.evergreen.yml")
	if err != nil {
		t.Fatalf("read .evergreen.yml: %v", err)
	}

	var doc struct {
		GithubPRAliases []struct {
			VariantTags []string `yaml:"variant_tags"`
			FilePattern string   `yaml:"file_pattern"`
		} `yaml:"github_pr_aliases"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse .evergreen.yml: %v", err)
	}

	for _, entry := range doc.GithubPRAliases {
		for _, tag := range entry.VariantTags {
			if tag == "pr_patch_e2e" {
				if entry.FilePattern == "" {
					t.Fatal("pr_patch_e2e alias has no file_pattern")
				}
				return regexp.MustCompile(entry.FilePattern)
			}
		}
	}
	t.Fatal("no pr_patch_e2e entry found in github_pr_aliases")
	return nil
}
