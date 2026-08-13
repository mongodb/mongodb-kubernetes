package helmchart

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestChartVersionMatchesReleaseJSON guards the rbac-version annotation contract: the chart
// version (rendered into the mongodb.com/rbac-version annotation on member-cluster resources)
// must equal release.json's mongodbOperator, which is the expected version injected into the
// operator binary at build time.
func TestChartVersionMatchesReleaseJSON(t *testing.T) {
	chartYAML, err := ChartFiles.ReadFile("Chart.yaml")
	require.NoError(t, err)
	var chart struct {
		Version string `json:"version"`
	}
	require.NoError(t, yaml.Unmarshal(chartYAML, &chart))

	releaseJSON, err := os.ReadFile(filepath.Join("..", "release.json"))
	require.NoError(t, err)
	var release struct {
		MongoDBOperator string `json:"mongodbOperator"`
	}
	require.NoError(t, json.Unmarshal(releaseJSON, &release))

	assert.Equal(t, release.MongoDBOperator, chart.Version,
		"helm_chart/Chart.yaml version must equal release.json's mongodbOperator")
}
