package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReleaseImages(t *testing.T) {
	tests := []struct {
		name        string
		buildInfo   string
		releaseJSON string
		wantNames   []string // expected images, sorted by Name
		wantVer     map[string]string
		wantErr     string
	}{
		{
			name: "selects only version-ref images and resolves versions",
			buildInfo: `{"images":{
				"operator":{"version-ref":"mongodbOperator","staging":{"repository":"ecr/staging/op","secondary-repositories":["quay/staging/op"]},"release":{"repository":"quay/op"}},
				"readiness-probe":{"version-ref":"readinessProbeVersion","staging":{"repository":"ecr/staging/rp"},"release":{"repository":"quay/rp"}},
				"meko-tests":{"staging":{"repository":"ecr/staging/t"},"release":{"repository":"ecr/staging/t"}},
				"agent":{"staging":{"repository":"ecr/staging/agent"},"release":{"repository":"quay/agent"}}
			}}`,
			releaseJSON: `{"mongodbOperator":"1.9.2","readinessProbeVersion":"1.0.24","agentVersion":"108.0.0"}`,
			wantNames:   []string{"operator", "readiness-probe"}, // expected image set members,
			wantVer:     map[string]string{"operator": "1.9.2", "readiness-probe": "1.0.24"},
		},
		{
			name: "version-ref without release repo is a hard error",
			buildInfo: `{"images":{
				"operator":{"version-ref":"mongodbOperator","staging":{"repository":"ecr/staging/op"},"release":{"repository":"quay/op"}},
				"broken":{"version-ref":"readinessProbeVersion","staging":{"repository":"ecr/staging/b"}}
			}}`,
			releaseJSON: `{"mongodbOperator":"1.9.2","readinessProbeVersion":"1.0.24"}`,
			wantErr:     `"broken" has version-ref but no release.repository`,
		},
		{
			name: "version-ref without staging repo is a hard error",
			buildInfo: `{"images":{
				"operator":{"version-ref":"mongodbOperator","staging":{"repository":"ecr/staging/op"},"release":{"repository":"quay/op"}},
				"broken":{"version-ref":"readinessProbeVersion","release":{"repository":"quay/b"}}
			}}`,
			releaseJSON: `{"mongodbOperator":"1.9.2","readinessProbeVersion":"1.0.24"}`,
			wantErr:     `"broken" has version-ref but no staging.repository`,
		},
		{
			name: "unresolvable version-ref is a hard error",
			buildInfo: `{"images":{
				"operator":{"version-ref":"mongodbOperator","staging":{"repository":"ecr/staging/op"},"release":{"repository":"quay/op"}},
				"readiness-probe":{"version-ref":"missingKey","staging":{"repository":"ecr/staging/rp"},"release":{"repository":"quay/rp"}}
			}}`,
			releaseJSON: `{"mongodbOperator":"1.9.2"}`,
			wantErr:     `version-ref "missingKey" not found`,
		},
		{
			name: "empty version string is a hard error",
			buildInfo: `{"images":{
				"operator":{"version-ref":"mongodbOperator","staging":{"repository":"ecr/staging/op"},"release":{"repository":"quay/op"}}
			}}`,
			releaseJSON: `{"mongodbOperator":""}`,
			wantErr:     `version-ref "mongodbOperator" is empty`,
		},
		{
			name: "no version-ref images at all is a hard error",
			buildInfo: `{"images":{
				"meko-tests":{"staging":{"repository":"ecr/staging/t"},"release":{"repository":"ecr/staging/t"}}
			}}`,
			releaseJSON: `{"mongodbOperator":"1.9.2"}`,
			wantErr:     "no release images",
		},
		{
			name: "missing anchor is a hard error",
			buildInfo: `{"images":{
				"readiness-probe":{"version-ref":"readinessProbeVersion","staging":{"repository":"ecr/staging/rp"},"release":{"repository":"quay/rp"}}
			}}`,
			releaseJSON: `{"readinessProbeVersion":"1.0.24"}`,
			wantErr:     `anchor image "operator" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			biPath := writeFile(t, dir, "build_info.json", tt.buildInfo)
			rjPath := writeFile(t, dir, "release.json", tt.releaseJSON)

			images, err := LoadReleaseImages(biPath, rjPath)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			gotNames := make([]string, len(images))
			for i, img := range images {
				gotNames[i] = img.Name
			}
			if strings.Join(gotNames, ",") != strings.Join(tt.wantNames, ",") {
				t.Fatalf("images: got %v, want %v", gotNames, tt.wantNames)
			}
			for _, img := range images {
				if want := tt.wantVer[img.Name]; want != "" && img.Version != want {
					t.Errorf("%s version: got %q, want %q", img.Name, img.Version, want)
				}
				if (img.Name == AnchorImageName) != img.IsAnchor {
					t.Errorf("%s IsAnchor: got %v", img.Name, img.IsAnchor)
				}
			}
		})
	}
}

func TestLoadReleaseImagesRealFiles(t *testing.T) {
	// Guards against drift between the checked-in build_info.json and release.json:
	// the six product images must load and version-resolve cleanly.
	biPath := filepath.Join("..", "..", "..", "build_info.json")
	rjPath := filepath.Join("..", "..", "..", "release.json")

	images, err := LoadReleaseImages(biPath, rjPath)
	if err != nil {
		t.Fatalf("loading checked-in files: %v", err)
	}

	want := map[string]bool{
		"operator": true, "init-database": true, "init-ops-manager": true,
		"database": true, "readiness-probe": true, "upgrade-hook": true,
	}
	if len(images) != len(want) {
		t.Errorf("image set size: got %d, want %d (%v)", len(images), len(want), images)
	}
	for _, img := range images {
		if !want[img.Name] {
			t.Errorf("unexpected image %q", img.Name)
		}
		if img.Version == "" || img.ReleaseRepo == "" || img.StagingRepo == "" {
			t.Errorf("%s under-populated: %+v", img.Name, img)
		}
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
