package searchcontroller

import (
	"bytes"
	"fmt"
	"text/template"
	"time"

	_ "embed"
)

//go:embed metrics_forwarder_otel_config.yaml.tmpl
var collectorConfigTemplate string

type MetricsForwarderConfigParams struct {
	OMBaseURL                         string
	HasOMCaCert                       bool
	RequireValidMMSServerCertificates bool
	ShardNames                        []string
	ClusterIndex                      int
	ClusterName                       string
	GroupID                           string
	MongotVersion                     string
	MongotName                        string
	MongotGRPCPort                    int
	// MongotTLSEnabled selects an HTTPS scrape with client-certificate
	// authentication for the mongot /metrics endpoint.
	MongotTLSEnabled bool
	// MongotTLSCAFile is the path to the CA bundle used to verify the mongot
	// server certificate. Only used when MongotTLSEnabled is true.
	MongotTLSCAFile string
	// MongotTLSCertFile / MongotTLSKeyFile are the paths to the forwarder client
	// certificate and key presented to mongot. Only used when MongotTLSEnabled is true.
	MongotTLSCertFile string
	MongotTLSKeyFile  string
	// ScrapeInterval is the global scrape_interval for the Prometheus receiver.
	// It must match prometheusDefaultScrapeInterval in the controller so the
	// hostDeletionDeferralWindow calculation stays correct.
	ScrapeInterval time.Duration
}

type MetricsForwarderOTelConfigTemplate struct {
	tmpl *template.Template
}

func NewMetricsForwarderOTelConfigTemplate() MetricsForwarderOTelConfigTemplate {
	tmpl := template.Must(template.New("metrics_forwarder_otel_config").Parse(collectorConfigTemplate))
	return MetricsForwarderOTelConfigTemplate{tmpl: tmpl}
}

func (t MetricsForwarderOTelConfigTemplate) Execute(params MetricsForwarderConfigParams) ([]byte, error) {
	var buf bytes.Buffer
	err := t.tmpl.Execute(&buf, params)
	if err != nil {
		return nil, fmt.Errorf("failed to execute metrics forwarder OTel config template: %w", err)
	}
	return bytes.Clone(buf.Bytes()), nil
}
