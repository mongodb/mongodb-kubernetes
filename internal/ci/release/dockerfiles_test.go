package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fullBuildInfo returns a BuildInfo with every key PlanDockerfileCopies expects.
func fullBuildInfo() *BuildInfo {
	return &BuildInfo{
		Images: map[string]BuildInfoImage{
			"operator":         {DockerfilePath: "docker/mongodb-kubernetes-operator/Dockerfile"},
			"database":         {DockerfilePath: "docker/mongodb-kubernetes-database/Dockerfile"},
			"init-database":    {DockerfilePath: "docker/mongodb-kubernetes-init-database/Dockerfile"},
			"init-ops-manager": {DockerfilePath: "docker/mongodb-kubernetes-init-ops-manager/Dockerfile"},
			"agent":            {DockerfilePath: "docker/mongodb-agent/Dockerfile"},
			"ops-manager":      {DockerfilePath: "docker/mongodb-enterprise-ops-manager/Dockerfile"},
		},
	}
}

func TestPlanDockerfileCopies_HappyPath(t *testing.T) {
	plan, err := PlanDockerfileCopies(fullBuildInfo(), "1.8.1", "public/dockerfiles")
	if err != nil {
		t.Fatalf("PlanDockerfileCopies: %v", err)
	}

	// 6 image keys, init-database has 2 public dirs, others have 1 → 7 entries.
	want := []DockerfileCopy{
		{"docker/mongodb-agent/Dockerfile", "public/dockerfiles/mongodb-agent/1.8.1/ubi/Dockerfile"},
		{"docker/mongodb-kubernetes-database/Dockerfile", "public/dockerfiles/mongodb-kubernetes-database/1.8.1/ubi/Dockerfile"},
		{"docker/mongodb-kubernetes-init-database/Dockerfile", "public/dockerfiles/mongodb-kubernetes-init-database/1.8.1/ubi/Dockerfile"},
		{"docker/mongodb-kubernetes-init-database/Dockerfile", "public/dockerfiles/mongodb-kubernetes-init-appdb/1.8.1/ubi/Dockerfile"},
		{"docker/mongodb-kubernetes-init-ops-manager/Dockerfile", "public/dockerfiles/mongodb-kubernetes-init-ops-manager/1.8.1/ubi/Dockerfile"},
		{"docker/mongodb-kubernetes-operator/Dockerfile", "public/dockerfiles/mongodb-kubernetes/1.8.1/ubi/Dockerfile"},
		{"docker/mongodb-enterprise-ops-manager/Dockerfile", "public/dockerfiles/mongodb-enterprise-ops-manager/1.8.1/ubi/Dockerfile"},
	}
	if len(plan) != len(want) {
		t.Fatalf("plan length: got %d, want %d\nplan: %+v", len(plan), len(want), plan)
	}
	for i, w := range want {
		if plan[i] != w {
			t.Errorf("plan[%d]: got %+v, want %+v", i, plan[i], w)
		}
	}
}

func TestPlanDockerfileCopies_MissingKey(t *testing.T) {
	bi := fullBuildInfo()
	delete(bi.Images, "agent")
	_, err := PlanDockerfileCopies(bi, "1.8.1", "public/dockerfiles")
	if err == nil {
		t.Fatal("expected error for missing image key")
	}
	if !strings.Contains(err.Error(), `missing image key "agent"`) {
		t.Errorf("error should name the missing key; got %v", err)
	}
}

func TestPlanDockerfileCopies_EmptyDockerfilePath(t *testing.T) {
	bi := fullBuildInfo()
	bi.Images["operator"] = BuildInfoImage{DockerfilePath: ""}
	_, err := PlanDockerfileCopies(bi, "1.8.1", "public/dockerfiles")
	if err == nil {
		t.Fatal("expected error for empty dockerfile-path")
	}
	if !strings.Contains(err.Error(), "empty dockerfile-path") {
		t.Errorf("error should mention empty dockerfile-path; got %v", err)
	}
}

func TestPlanDockerfileCopies_EmptyVersion(t *testing.T) {
	_, err := PlanDockerfileCopies(fullBuildInfo(), "", "public/dockerfiles")
	if err == nil {
		t.Fatal("expected error for empty version")
	}
}

func TestPlanDockerfileCopies_NilBuildInfo(t *testing.T) {
	_, err := PlanDockerfileCopies(nil, "1.8.1", "public/dockerfiles")
	if err == nil {
		t.Fatal("expected error for nil build info")
	}
}

func TestCopyDockerfiles_WritesFilesAndCreatesDirs(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	srcPath := filepath.Join(srcDir, "Dockerfile")
	const content = "FROM scratch\n# marker line\n"
	if err := os.WriteFile(srcPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	dstPath := filepath.Join(tmp, "dest", "image", "1.0.0", "ubi", "Dockerfile")
	plan := []DockerfileCopy{{Src: srcPath, Dst: dstPath}}

	if err := CopyDockerfiles(plan); err != nil {
		t.Fatalf("CopyDockerfiles: %v", err)
	}
	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != content {
		t.Errorf("content mismatch:\n got %q\nwant %q", string(got), content)
	}
}

func TestCopyDockerfiles_MissingSourceFails(t *testing.T) {
	tmp := t.TempDir()
	plan := []DockerfileCopy{{
		Src: filepath.Join(tmp, "does-not-exist"),
		Dst: filepath.Join(tmp, "out", "Dockerfile"),
	}}
	err := CopyDockerfiles(plan)
	if err == nil {
		t.Fatal("expected error for missing source")
	}
	if !strings.Contains(err.Error(), "source missing") {
		t.Errorf("error should mention 'source missing'; got %v", err)
	}
}
