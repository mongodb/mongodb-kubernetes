package searchcontroller

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// renderDirEnv names the directory the rendered collector configs are written to.
// The test is skipped unless it is set: it is a helper for the third-party image bump workflow
// (.github/workflows/bump-third-party-images.yml), which validates the rendered configs with
// `otelcol-contrib validate` from the candidate image. It never writes into the repository.
const renderDirEnv = "OTEL_CONFIG_RENDER_DIR"

func TestRenderMetricsForwarderConfigsForValidation(t *testing.T) {
	dir := os.Getenv(renderDirEnv)
	if dir == "" {
		t.Skipf("%s is not set; this test only renders configs for the image bump validation", renderDirEnv)
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))

	base := MetricsForwarderConfigParams{
		OMBaseURL:                         "https://ops-manager.example.com:8443",
		HasOMCaCert:                       true,
		RequireValidMMSServerCertificates: true,
		ClusterIndex:                      0,
		ClusterName:                       "cluster-0",
		GroupID:                           "5f1b2c3d4e5f6a7b8c9d0e1f",
		MongotVersion:                     "1.70.1",
		MongotName:                        "mdbs",
		MongotGRPCPort:                    27028,
		ScrapeInterval:                    60 * time.Second,
	}
	sharded := base
	sharded.ShardNames = []string{"shard-0", "shard-1"}

	tmpl := NewMetricsForwarderOTelConfigTemplate()
	for name, params := range map[string]MetricsForwarderConfigParams{
		"replicaset.yaml": base,
		"sharded.yaml":    sharded,
	} {
		rendered, err := tmpl.Execute(params)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), rendered, 0o644))
	}
}
