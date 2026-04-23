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
	assert.ErrorContains(t, err, "input cancelled")
}

func tlsEnabledAC() *om.AutomationConfig {
	return om.NewAutomationConfig(om.Deployment{
		"processes": []any{
			map[string]any{
				"name":        "host-0",
				"processType": "mongod",
				"args2_6": map[string]any{
					"net": map[string]any{
						"tls": map[string]any{"mode": "requireSSL"},
					},
				},
			},
		},
		"replicaSets": []any{},
	})
}

func TestEnsureTLS_InvalidFlagReturnsError(t *testing.T) {
	ac := tlsEnabledAC()
	opts := &GenerateOptions{}
	err := ensureTLS(ac, opts, nil, "Invalid_Name!")
	assert.ErrorContains(t, err, "not a valid Kubernetes resource name")
}

func TestEnsureTLS_InvalidInteractiveInputReprompts(t *testing.T) {
	ac := tlsEnabledAC()
	opts := &GenerateOptions{}
	scanner := bufio.NewScanner(strings.NewReader("Invalid_Name!\nmdb\n"))
	err := ensureTLS(ac, opts, scanner, "")
	require.NoError(t, err)
	assert.Equal(t, "mdb", opts.CertsSecretPrefix)
}

func TestEnsureTLS_ValidFlagUsedDirectly(t *testing.T) {
	ac := tlsEnabledAC()
	opts := &GenerateOptions{}
	err := ensureTLS(ac, opts, nil, "mdb")
	require.NoError(t, err)
	assert.Equal(t, "mdb", opts.CertsSecretPrefix)
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
					"args2_6": map[string]any{
						"net": map[string]any{
							"tls": map[string]any{
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
			"args2_6": map[string]any{
				"net": map[string]any{
					"ssl": map[string]any{
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
			"args2_6": map[string]any{},
		},
	}
	assert.False(t, isTLSEnabled(processMap))
}
