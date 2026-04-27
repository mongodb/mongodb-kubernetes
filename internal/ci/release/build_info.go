package release

import (
	"encoding/json"
	"fmt"
	"os"
)

// BuildInfo is a minimal projection of build_info.json — only the fields the
// release tooling currently consumes. Add more as commands need them; do not
// turn this into a 1:1 mirror of the JSON.
type BuildInfo struct {
	Images map[string]BuildInfoImage `json:"images"`
}

// BuildInfoImage captures the per-image fields used by mck-ci subcommands.
// scenario-specific blocks (patch/staging/release) are intentionally not
// modeled here yet.
type BuildInfoImage struct {
	DockerfilePath string `json:"dockerfile-path"`
}

// ReadBuildInfo parses build_info.json at path. Returns a typed error if the
// file is missing or malformed.
func ReadBuildInfo(path string) (*BuildInfo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var bi BuildInfo
	if err := json.Unmarshal(raw, &bi); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &bi, nil
}
