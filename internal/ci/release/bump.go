package release

import (
	"fmt"
	"os"
	"regexp"
)

// operatorVersionPattern captures the `"mongodbOperator": "<value>"` line in
// release.json. The first group is the prefix (key + colon + spaces + opening
// quote area), the second group is the value. Replacement preserves the
// surrounding whitespace and quoting style of the file.
var operatorVersionPattern = regexp.MustCompile(`("mongodbOperator"\s*:\s*)"([^"]*)"`)

// BumpOperatorVersion sets release.json's mongodbOperator field to newVersion,
// preserving all other content and formatting. It returns the previous version
// and a `changed` flag (false if the file was already at newVersion, in which
// case no write occurs).
func BumpOperatorVersion(path, newVersion string) (oldVersion string, changed bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", path, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	matches := operatorVersionPattern.FindAllSubmatchIndex(raw, -1)
	switch len(matches) {
	case 0:
		return "", false, fmt.Errorf("%s: mongodbOperator field not found", path)
	case 1:
		// expected
	default:
		return "", false, fmt.Errorf("%s: expected exactly one mongodbOperator field, found %d", path, len(matches))
	}
	submatch := operatorVersionPattern.FindSubmatch(raw)
	oldVersion = string(submatch[2])
	if oldVersion == newVersion {
		return oldVersion, false, nil
	}
	updated := operatorVersionPattern.ReplaceAll(raw, []byte(`${1}"`+newVersion+`"`))
	if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		return "", false, fmt.Errorf("write %s: %w", path, err)
	}
	return oldVersion, true, nil
}
