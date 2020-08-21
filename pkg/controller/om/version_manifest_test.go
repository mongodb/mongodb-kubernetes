package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersionManifest_CutLegacyVersions(t *testing.T) {
	provider := FileManifestProvider{FilePath: "../operator/testdata/version_manifest.json"}
	manifest, err := provider.GetVersion()
	assert.NoError(t, err)
	var versions []string
	for _, version := range manifest.Versions {
		versions = append(versions, version.Name)
		assert.Greater(t, len(version.Builds), 0)
	}
	// 2.6 versions were cut, all the others were copied
	assert.Equal(t, []string{"3.6.0", "3.6.0-ent", "4.2.2-ent"}, versions)
}
