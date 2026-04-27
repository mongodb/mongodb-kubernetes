package release

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
)

//go:embed pr_body.tpl.md
var prBodyRaw string

var prBodyTpl = template.Must(template.New("pr-body").Parse(prBodyRaw))

// PRTitle returns the canonical title for an MCK release PR.
func PRTitle(version string) string {
	return fmt.Sprintf("Release MCK %s", version)
}

// RenderPRBody fills the embedded markdown template with the given version.
func RenderPRBody(version string) (string, error) {
	var buf bytes.Buffer
	if err := prBodyTpl.Execute(&buf, struct{ Version string }{Version: version}); err != nil {
		return "", fmt.Errorf("render PR body: %w", err)
	}
	return buf.String(), nil
}
