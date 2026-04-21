package migratetomck

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func TestBuildMongodbOptions_FlagTranslation(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})

	f := mongodbFlags{
		configMapName:        "my-cm",
		secretName:           "my-secret",
		namespace:            "mongodb",
		resourceNameOverride: "my-rs",
	}
	opts, err := buildMongodbOptions(context.Background(), nil, ac, &ProjectConfigs{}, nil, strings.NewReader(""), f)
	require.NoError(t, err)
	assert.Equal(t, "my-cm", opts.ConfigMapName)
	assert.Equal(t, "my-secret", opts.CredentialsSecretName)
	assert.Equal(t, "mongodb", opts.Namespace)
	assert.Equal(t, "my-rs", opts.ResourceNameOverride)
}

func TestBuildMongodbOptions_InvalidMultiClusterNames(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})

	f := mongodbFlags{multiClusterNames: "  ,  ,  "}
	_, err := buildMongodbOptions(context.Background(), nil, ac, &ProjectConfigs{}, nil, strings.NewReader(""), f)
	assert.ErrorContains(t, err, "no valid cluster names")
}

func TestCollectPrometheusCreds_NoPrometheus(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	opts := &GenerateOptions{Namespace: "mongodb"}
	err := collectPrometheusCreds(context.Background(), nil, ac, opts, nil, "")
	require.NoError(t, err)
	assert.Empty(t, opts.PrometheusPassword)
	assert.Empty(t, opts.PrometheusSecretName)
}

func TestCollectPrometheusCreds_InteractivePassword(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
		"prometheus":  map[string]any{"enabled": true, "username": "prom-user"},
	})
	opts := &GenerateOptions{Namespace: "mongodb"}
	scanner := bufio.NewScanner(strings.NewReader("supersecret\n"))
	err := collectPrometheusCreds(context.Background(), nil, ac, opts, scanner, "")
	require.NoError(t, err)
	assert.Equal(t, "supersecret", opts.PrometheusPassword)
}

func TestCollectPrometheusCreds_EmptyPassword(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
		"prometheus":  map[string]any{"enabled": true, "username": "prom-user"},
	})
	opts := &GenerateOptions{Namespace: "mongodb"}
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	err := collectPrometheusCreds(context.Background(), nil, ac, opts, scanner, "")
	assert.ErrorContains(t, err, "cannot be empty")
}

func TestIsTLSEnabled_TLSMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		enabled bool
	}{
		{"requireSSL", "requireSSL", true},
		{"requireTLS", "requireTLS", true},
		{"preferSSL", "preferSSL", true},
		{"preferTLS", "preferTLS", true},
		{"allowSSL", "allowSSL", true},
		{"allowTLS", "allowTLS", true},
		{"disabled", "disabled", false},
		{"empty defaults to require", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processMap := map[string]om.Process{
				"host-0": {
					"args2_6": map[string]interface{}{
						"net": map[string]interface{}{
							"tls": map[string]interface{}{
								"mode": tt.mode,
							},
						},
					},
				},
			}
			assert.Equal(t, tt.enabled, isTLSEnabled(processMap))
		})
	}
}

func TestIsTLSEnabled_SSLMode(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"ssl": map[string]interface{}{
						"mode": "requireSSL",
					},
				},
			},
		},
	}
	assert.True(t, isTLSEnabled(processMap))
}

func TestIsTLSEnabled_NoArgs(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	assert.False(t, isTLSEnabled(processMap))
}

func TestIsTLSEnabled_NoNet(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{},
		},
	}
	assert.False(t, isTLSEnabled(processMap))
}
