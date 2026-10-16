package searchcontroller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsForwarderOTelConfig_MetricsTLS(t *testing.T) {
	tmpl := NewMetricsForwarderOTelConfigTemplate()

	t.Run("TLS disabled renders plain HTTP scrape", func(t *testing.T) {
		out, err := tmpl.Execute(MetricsForwarderConfigParams{
			MongotName:     "search",
			ClusterIndex:   0,
			ScrapeInterval: time.Minute,
		})
		require.NoError(t, err)
		assert.Contains(t, string(out), "metrics_path: /metrics")
		assert.NotContains(t, string(out), "scheme: https")
		assert.NotContains(t, string(out), "tls_config:")
	})

	t.Run("TLS enabled renders HTTPS scrape with client cert and CA", func(t *testing.T) {
		out, err := tmpl.Execute(MetricsForwarderConfigParams{
			MongotName:        "search",
			ClusterIndex:      0,
			ScrapeInterval:    time.Minute,
			MongotTLSEnabled:  true,
			MongotTLSCAFile:   "/mongodb-automation/metrics-server-ca/ca.crt",
			MongotTLSCertFile: "/mongodb-automation/metrics-client-cert/tls.crt",
			MongotTLSKeyFile:  "/mongodb-automation/metrics-client-cert/tls.key",
		})
		require.NoError(t, err)
		assert.Contains(t, string(out), "scheme: https")
		assert.Contains(t, string(out), "tls_config:")
		assert.Contains(t, string(out), "ca_file: /mongodb-automation/metrics-server-ca/ca.crt")
		assert.Contains(t, string(out), "cert_file: /mongodb-automation/metrics-client-cert/tls.crt")
		assert.Contains(t, string(out), "key_file: /mongodb-automation/metrics-client-cert/tls.key")
	})
}
