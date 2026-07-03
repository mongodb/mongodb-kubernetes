package evergreen_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestE2EVariantPaths(t *testing.T) {
	patterns := loadE2EVariantPaths(t)

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
			desc:        "docker package Go",
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
				if matchesPaths(patterns, f) {
					matched = true
					break
				}
			}
			if matched != tc.shouldMatch {
				if tc.shouldMatch {
					t.Errorf("expected at least one of %v to be matched by paths %v, but none were", tc.files, patterns)
				} else {
					t.Errorf("expected none of %v to be matched by paths %v, but at least one was", tc.files, patterns)
				}
			}
		})
	}
}

// loadE2EVariantPaths reads the paths patterns from the first pr_patch_e2e
// build variant in .evergreen.yml, so the test stays in sync with the real config.
func loadE2EVariantPaths(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("../../../.evergreen.yml")
	if err != nil {
		t.Fatalf("read .evergreen.yml: %v", err)
	}

	var doc struct {
		BuildVariants []struct {
			Tags  []string `yaml:"tags"`
			Paths []string `yaml:"paths"`
		} `yaml:"buildvariants"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse .evergreen.yml: %v", err)
	}

	for _, v := range doc.BuildVariants {
		for _, tag := range v.Tags {
			if tag == "pr_patch_e2e" {
				if len(v.Paths) == 0 {
					t.Fatal("pr_patch_e2e variant has no paths defined")
				}
				return v.Paths
			}
		}
	}
	t.Fatal("no pr_patch_e2e variant found in buildvariants")
	return nil
}

// matchesPaths evaluates gitignore-style path patterns against a file.
// Supports "**/*.ext" (match any file with extension) and "!prefix/**" (exclude).
func matchesPaths(patterns []string, file string) bool {
	matched := false
	for _, p := range patterns {
		negate := strings.HasPrefix(p, "!")
		pattern := strings.TrimPrefix(p, "!")

		var hits bool
		switch {
		case strings.HasPrefix(pattern, "**/"):
			// "**/*.go" — file must end with the suffix after "**/"
			suffix := pattern[3:]                    // e.g. "*.go"
			suffix = strings.TrimPrefix(suffix, "*") // e.g. ".go"
			hits = strings.HasSuffix(file, suffix)
		case strings.HasSuffix(pattern, "/**"):
			// "ci/**" — file must be under the prefix directory
			prefix := strings.TrimSuffix(pattern, "/**")
			hits = strings.HasPrefix(file, prefix+"/")
		}

		if hits {
			matched = !negate
		}
	}
	return matched
}
