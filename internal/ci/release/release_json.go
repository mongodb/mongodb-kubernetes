// Package release contains pure (non-I/O-orchestrating) logic for MCK
// release automation: parsing release.json, validating release preconditions,
// rendering PR bodies. Side-effecting code (git, gh, make) lives in the cli
// orchestrators that consume this package.
package release

import (
	"encoding/json"
	"fmt"
	"os"
)

// ReadOperatorVersion reads release.json at path and returns the
// `mongodbOperator` field. Returns a typed error if the file is missing,
// malformed, or the field is absent or empty.
func ReadOperatorVersion(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		MongoDBOperator string `json:"mongodbOperator"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.MongoDBOperator == "" {
		return "", fmt.Errorf("%s: mongodbOperator field is missing or empty", path)
	}
	return doc.MongoDBOperator, nil
}
