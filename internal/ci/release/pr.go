package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultReleasedImages are the supportedImages keys updated on every operator release.
var DefaultReleasedImages = []string{
	"mongodb-kubernetes",
	"init-ops-manager",
	"init-database",
	"database",
}

// PROpener branches, commits, pushes, and opens a pull request, returning its URL.
type PROpener interface {
	Open(repoRoot, branch, title, body string) (prURL string, err error)
}

// PRInputs are the parameters for a release PR operation.
type PRInputs struct {
	Version  string // required
	RepoRoot string // path to repo root; if empty, auto-detected from cwd
}

// AppendVersionToImages appends version to the versions array of each named image
// in the release.json content. Returns an error if any image is not found.
// The operation is idempotent: if version is already present it is not duplicated.
func AppendVersionToImages(data []byte, images []string, version string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse release.json: %w", err)
	}

	var si map[string]json.RawMessage
	if err := json.Unmarshal(doc["supportedImages"], &si); err != nil {
		return nil, fmt.Errorf("parse supportedImages: %w", err)
	}

	for _, imgName := range images {
		raw, ok := si[imgName]
		if !ok {
			return nil, fmt.Errorf("image %q not found in supportedImages", imgName)
		}

		var imgDoc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &imgDoc); err != nil {
			return nil, fmt.Errorf("parse image %q: %w", imgName, err)
		}

		var versions []string
		if err := json.Unmarshal(imgDoc["versions"], &versions); err != nil {
			return nil, fmt.Errorf("parse versions for %q: %w", imgName, err)
		}

		if slices.Contains(versions, version) {
			continue
		}

		versions = append(versions, version)
		versionsRaw, err := json.Marshal(versions)
		if err != nil {
			return nil, err
		}
		imgDoc["versions"] = versionsRaw

		imgRaw, err := json.Marshal(imgDoc)
		if err != nil {
			return nil, err
		}
		si[imgName] = imgRaw
	}

	siRaw, err := json.Marshal(si)
	if err != nil {
		return nil, err
	}
	doc["supportedImages"] = siRaw

	result, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}

// ReleasePR updates release.json on disk then delegates branching, committing,
// pushing, and PR creation entirely to the opener.
func ReleasePR(inputs PRInputs, opener PROpener) (string, error) {
	if inputs.Version == "" {
		return "", errors.New("version is required")
	}

	repoRoot := inputs.RepoRoot
	if repoRoot == "" {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return "", fmt.Errorf("not in a git repository: %w", err)
		}
		repoRoot = strings.TrimSpace(string(out))
	}

	releaseJSONPath := filepath.Join(repoRoot, "release.json")
	data, err := os.ReadFile(releaseJSONPath)
	if err != nil {
		return "", fmt.Errorf("read release.json: %w", err)
	}

	updated, err := AppendVersionToImages(data, DefaultReleasedImages, inputs.Version)
	if err != nil {
		return "", fmt.Errorf("update release.json: %w", err)
	}

	if err := os.WriteFile(releaseJSONPath, updated, 0o644); err != nil {
		return "", fmt.Errorf("write release.json: %w", err)
	}

	branch := "release-" + inputs.Version
	title := "Release " + inputs.Version
	body := fmt.Sprintf("Adds operator version %s to `release.json` supported image lists.", inputs.Version)
	return opener.Open(repoRoot, branch, title, body)
}
