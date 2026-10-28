package vault

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidInjectionSecretName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain name", "my-secret", true},
		{"operator-generated", "my-rs-agent-certs-operator-generated", true},
		{"prefixed", "prefix-my-rs-agent-pem", true},
		{"empty", "", false},
		{"quote breakout", `x" }}{{ with secret "secret/data/other/tls" }}{{ .Data.data.tls.key }}{{ end }}{{ "`, false},
		{"template directive", `{{ secret "foo" }}`, false},
		{"space", "my secret", false},
		{"slash path traversal", "../operator/admin", false},
		{"uppercase", "MySecret", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsValidInjectionSecretName(tt.input))
		})
	}
}

func TestDatabaseAnnotationsRejectsTemplateInjection(t *testing.T) {
	payload := `x" }}{{ with secret "secret/data/other/tls" }}{{ .Data.data.tls.key }}{{ end }}{{ "`

	s := DatabaseSecretsToInject{
		AgentCerts:     payload,
		AgentCertsHash: "hash",
		AgentApiKey:    "agent-api-key",
	}
	annotations := s.DatabaseAnnotations("tenant-a")

	for key, value := range annotations {
		assert.NotContains(t, value, payload, "annotation %s leaks the payload", key)
	}
	// the whole agent-certs block must be skipped
	assert.NotContains(t, annotations, "vault.hashicorp.com/agent-inject-secret-mms-automation-agent-pem")
	assert.NotContains(t, annotations, "vault.hashicorp.com/agent-inject-template-mms-automation-agent-pem")
	assert.NotContains(t, annotations, "vault.hashicorp.com/agent-inject-template-previous-mms-automation-agent-pem")
	// unrelated entries (operator-derived API key) are unaffected
	assert.Contains(t, annotations, "vault.hashicorp.com/agent-inject-template-agentApiKey")
}

func TestDatabaseAnnotationsValidNamesUnchanged(t *testing.T) {
	s := DatabaseSecretsToInject{
		AgentCerts:     "certs-prefix-rs-agent-pem-operator-generated",
		AgentCertsHash: "hash",
		AgentApiKey:    "agent-api-key",
	}
	annotations := s.DatabaseAnnotations("tenant-a")

	assert.Contains(t, annotations, "vault.hashicorp.com/agent-inject-template-mms-automation-agent-pem")
	assert.Contains(t,
		annotations["vault.hashicorp.com/agent-inject-template-mms-automation-agent-pem"],
		`secret "/secret/data/mongodbenterprise/database/tenant-a/certs-prefix-rs-agent-pem-operator-generated"`)
}

func TestOpsManagerAnnotationsRejectsTemplateInjection(t *testing.T) {
	payload := `x" }}{{ with secret "secret/data/other/tls" }}{{ end }}{{ "`
	s := OpsManagerSecretsToInject{
		TLSSecretName:         payload,
		TLSHash:               "hash",
		AppDBConnection:       "om-appdb-connection-string",
		AppDBConnectionVolume: "/mnt/conn",
	}
	annotations := s.OpsManagerAnnotations("tenant-a")
	for key, value := range annotations {
		assert.NotContains(t, value, payload, "annotation %s leaks the payload", key)
	}
	assert.NotContains(t, annotations, "vault.hashicorp.com/agent-inject-template-om-tls-cert-pem")
	// operator-derived connection string entry unaffected
	assert.Contains(t, annotations, "vault.hashicorp.com/agent-inject-secret-appdb-connection-string")
}

func TestAppDBAnnotationsRejectsTemplateInjection(t *testing.T) {
	payload := `x" }}{{ with secret "secret/data/other/tls" }}{{ end }}{{ "`
	s := AppDBSecretsToInject{
		TLSSecretName:              payload,
		TLSClusterHash:             "hash",
		AutomationConfigSecretName: "om-automation-config",
		AutomationConfigPath:       "automation-config",
		AgentType:                  "automation-agent",
	}
	annotations := s.AppDBAnnotations("tenant-a")
	for key, value := range annotations {
		assert.NotContains(t, value, payload, "annotation %s leaks the payload", key)
	}
	assert.NotContains(t, annotations, "vault.hashicorp.com/agent-inject-template-tls-certificate")
	assert.Contains(t, annotations, "vault.hashicorp.com/agent-inject-secret-automation-agent")
}
