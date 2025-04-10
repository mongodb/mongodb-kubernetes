package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
)

func TestReadProxyVarsFromEnv(t *testing.T) {
	tests := []struct {
		name         string
		operatorEnv  map[string]string
		expectedVars []corev1.EnvVar
	}{
		{
			name: "Do not propagate proxy env explicitly",
			operatorEnv: map[string]string{
				"MDB_PROPAGATE_PROXY_ENV": "false",
				"NO_PROXY":                "google.com",
			},
			expectedVars: nil,
		},
		{
			name: "Do not propagate proxy env by default",
			operatorEnv: map[string]string{
				"HTTP_PROXY":  "http://example-http-proxy:7312",
				"HTTPS_PROXY": "https://secure-proxy:3242",
			},
			expectedVars: nil,
		},
		{
			name: "Propagate proxy environment variables",
			operatorEnv: map[string]string{
				"MDB_PROPAGATE_PROXY_ENV": "true",
				"HTTP_PROXY":              "http://example-http-proxy:7312",
				"HTTPS_PROXY":             "https://secure-proxy:3242",
			},
			expectedVars: []corev1.EnvVar{
				{
					Name:  "HTTP_PROXY",
					Value: "http://example-http-proxy:7312",
				},
				{
					Name:  "http_proxy",
					Value: "http://example-http-proxy:7312",
				},
				{
					Name:  "HTTPS_PROXY",
					Value: "https://secure-proxy:3242",
				},
				{
					Name:  "https_proxy",
					Value: "https://secure-proxy:3242",
				},
			},
		},
		{
			name: "Propagate only proxy environment variables",
			operatorEnv: map[string]string{
				"MDB_PROPAGATE_PROXY_ENV": "true",
				"HTTPS_PROXY":             "https://secure-proxy:3242",
				"DEFAULT_AGENT_VERSION":   "13.0.2341",
				"MAX_SURGE":               "23415",
			},
			expectedVars: []corev1.EnvVar{
				{
					Name:  "HTTPS_PROXY",
					Value: "https://secure-proxy:3242",
				},
				{
					Name:  "https_proxy",
					Value: "https://secure-proxy:3242",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, val := range tt.operatorEnv {
				t.Setenv(key, val)
			}
			assert.Equal(t, ReadDatabaseProxyVarsFromEnv(), tt.expectedVars)
		})
	}
}
