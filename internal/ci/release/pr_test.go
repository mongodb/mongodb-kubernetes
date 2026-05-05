package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// --- fake ---

type fakePROpener struct {
	calls []prOpenCall
	url   string
	err   error
}

type prOpenCall struct {
	repoRoot, branch, title, body string
}

func (f *fakePROpener) Open(repoRoot, branch, title, body string) (string, error) {
	f.calls = append(f.calls, prOpenCall{repoRoot, branch, title, body})
	return f.url, f.err
}

// --- helpers ---

const releaseJSONFixture = `{
  "mongodbOperator": "1.8.0",
  "supportedImages": {
    "mongodb-kubernetes": {
      "versions": ["1.7.0", "1.8.0"]
    },
    "init-ops-manager": {
      "versions": ["1.7.0", "1.8.0"]
    },
    "init-database": {
      "versions": ["1.7.0", "1.8.0"]
    },
    "database": {
      "versions": ["1.7.0", "1.8.0"]
    }
  }
}`

// initRepoWithReleaseJSON creates a temp git repo containing release.json and returns its path.
func initRepoWithReleaseJSON(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initRepo(t, dir)
	path := filepath.Join(dir, "release.json")
	if err := os.WriteFile(path, []byte(releaseJSONFixture), 0o644); err != nil {
		t.Fatalf("write release.json: %v", err)
	}
	mustGit(t, dir, "add", "release.json")
	mustGit(t, dir, "commit", "-m", "add release.json")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// versionsInImage parses release.json and returns the versions array for the given image.
func versionsInImage(t *testing.T, data []byte, image string) []string {
	t.Helper()
	var doc struct {
		SupportedImages map[string]struct {
			Versions []string `json:"versions"`
		} `json:"supportedImages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	img, ok := doc.SupportedImages[image]
	if !ok {
		t.Fatalf("image %q not found in release.json", image)
	}
	return img.Versions
}

func assertContainsVersion(t *testing.T, data []byte, image, version string) {
	t.Helper()
	if !slices.Contains(versionsInImage(t, data, image), version) {
		t.Errorf("version %q not found in image %q", version, image)
	}
}

func assertNoDuplicates(t *testing.T, data []byte, image string) {
	t.Helper()
	seen := map[string]int{}
	for _, v := range versionsInImage(t, data, image) {
		seen[v]++
	}
	for v, count := range seen {
		if count > 1 {
			t.Errorf("version %q appears %d times in image %q", v, count, image)
		}
	}
}

// --- AppendVersionToImages ---

func TestAppendVersionToImages(t *testing.T) {
	tests := []struct {
		name    string
		images  []string
		version string
		check   func(t *testing.T, result []byte)
		wantErr string
	}{
		{
			name:    "appends to all specified images",
			images:  []string{"mongodb-kubernetes", "init-database"},
			version: "1.9.0",
			check: func(t *testing.T, result []byte) {
				assertContainsVersion(t, result, "mongodb-kubernetes", "1.9.0")
				assertContainsVersion(t, result, "init-database", "1.9.0")
			},
		},
		{
			name:    "leaves unspecified images untouched",
			images:  []string{"mongodb-kubernetes"},
			version: "1.9.0",
			check: func(t *testing.T, result []byte) {
				versions := versionsInImage(t, result, "init-database")
				for _, v := range versions {
					if v == "1.9.0" {
						t.Error("init-database should not have been updated")
					}
				}
			},
		},
		{
			name:    "idempotent — skips already present version",
			images:  []string{"mongodb-kubernetes"},
			version: "1.8.0",
			check: func(t *testing.T, result []byte) {
				assertNoDuplicates(t, result, "mongodb-kubernetes")
			},
		},
		{
			name:    "preserves existing versions",
			images:  []string{"mongodb-kubernetes"},
			version: "1.9.0",
			check: func(t *testing.T, result []byte) {
				assertContainsVersion(t, result, "mongodb-kubernetes", "1.7.0")
				assertContainsVersion(t, result, "mongodb-kubernetes", "1.8.0")
			},
		},
		{
			name:    "unknown image returns error",
			images:  []string{"nonexistent-image"},
			version: "1.9.0",
			wantErr: "nonexistent-image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := AppendVersionToImages([]byte(releaseJSONFixture), tt.images, tt.version)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, result)
		})
	}
}

// --- ReleasePR ---

func TestReleasePR(t *testing.T) {
	tests := []struct {
		name    string
		inputs  PRInputs
		wantErr string
	}{
		{
			name: "happy path",
			inputs: PRInputs{
				Version: "1.9.0",
			},
		},
		{
			name:    "version required",
			inputs:  PRInputs{},
			wantErr: "version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initRepoWithReleaseJSON(t)
			tt.inputs.RepoRoot = dir

			opener := &fakePROpener{url: "https://github.com/org/repo/pull/42"}
			prURL, err := ReleasePR(tt.inputs, opener)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// returned the opener's URL
			if prURL != opener.url {
				t.Errorf("prURL: got %q, want %q", prURL, opener.url)
			}

			// release.json on disk has the new version for all default images
			data, _ := os.ReadFile(filepath.Join(dir, "release.json"))
			for _, img := range DefaultReleasedImages {
				assertContainsVersion(t, data, img, tt.inputs.Version)
			}

			// opener called once with correct repoRoot, branch, and title
			if len(opener.calls) != 1 {
				t.Fatalf("expected 1 PR open call, got %d", len(opener.calls))
			}
			call := opener.calls[0]
			if call.repoRoot != dir {
				t.Errorf("repoRoot: got %q, want %q", call.repoRoot, dir)
			}
			if !strings.Contains(call.branch, tt.inputs.Version) {
				t.Errorf("branch %q does not contain version %q", call.branch, tt.inputs.Version)
			}
			if !strings.Contains(call.title, tt.inputs.Version) {
				t.Errorf("title %q does not contain version %q", call.title, tt.inputs.Version)
			}
		})
	}
}
