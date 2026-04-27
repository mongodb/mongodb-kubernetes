package release

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// PublicDirsByImageKey maps a build_info.json image key to the public-dir
// names under which the rendered Dockerfile should be published.
//
// REVIEW: the init-database → init-appdb dual-publish is not represented in
// build_info.json today. If this mapping moves into build_info.json (or a
// dedicated release manifest), this table should defer to that source of
// truth instead of duplicating it here.
var PublicDirsByImageKey = map[string][]string{
	"operator":         {"mongodb-kubernetes"},
	"database":         {"mongodb-kubernetes-database"},
	"init-database":    {"mongodb-kubernetes-init-database", "mongodb-kubernetes-init-appdb"},
	"init-ops-manager": {"mongodb-kubernetes-init-ops-manager"},
	"agent":            {"mongodb-agent"},
	"ops-manager":      {"mongodb-enterprise-ops-manager"},
}

// DockerfileCopy is a single planned copy operation.
type DockerfileCopy struct {
	Src string
	Dst string
}

// PlanDockerfileCopies builds the (src, dst) pairs the release tooling needs
// to perform, *without* doing any I/O. Returning a plan keeps the side-effect
// step (CopyDockerfiles) trivial and lets tests assert what would be copied.
//
// Output is deterministic (sorted by image key, then preserving the order of
// public dirs configured for that key) so logs and golden-file tests stay
// stable across runs.
func PlanDockerfileCopies(bi *BuildInfo, version, destRoot string) ([]DockerfileCopy, error) {
	if version == "" {
		return nil, fmt.Errorf("version must not be empty")
	}
	if bi == nil || bi.Images == nil {
		return nil, fmt.Errorf("build_info has no images")
	}

	keys := make([]string, 0, len(PublicDirsByImageKey))
	for k := range PublicDirsByImageKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var plan []DockerfileCopy
	for _, key := range keys {
		img, ok := bi.Images[key]
		if !ok {
			return nil, fmt.Errorf("build_info: missing image key %q", key)
		}
		if img.DockerfilePath == "" {
			return nil, fmt.Errorf("build_info: image %q has empty dockerfile-path", key)
		}
		for _, publicDir := range PublicDirsByImageKey[key] {
			plan = append(plan, DockerfileCopy{
				Src: img.DockerfilePath,
				Dst: filepath.Join(destRoot, publicDir, version, "ubi", "Dockerfile"),
			})
		}
	}
	return plan, nil
}

// CopyDockerfiles executes a plan produced by PlanDockerfileCopies. Parent
// directories are created as needed. File mode is preserved from the source.
func CopyDockerfiles(plan []DockerfileCopy) error {
	for _, p := range plan {
		info, err := os.Stat(p.Src)
		if err != nil {
			return fmt.Errorf("source missing %s: %w", p.Src, err)
		}
		if err := os.MkdirAll(filepath.Dir(p.Dst), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(p.Dst), err)
		}
		if err := copyFile(p.Src, p.Dst, info.Mode()); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", p.Src, p.Dst, err)
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()

	_, err = io.Copy(out, in)
	return err
}
