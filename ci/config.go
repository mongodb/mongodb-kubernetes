// Package ci embeds repo-level CI configuration so mckci can read it without
// resolving filesystem paths at runtime. The embedded data reflects the commit
// the binary was built from.
package ci

import _ "embed"

// BackportingYAML is the raw contents of ci/backporting.yaml.
//
//go:embed backporting.yaml
var BackportingYAML []byte
