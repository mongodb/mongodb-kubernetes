package vault

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
	"github.com/hashicorp/vault/api"
)

const (
	VaultBackend     = "VAULT_BACKEND"
	K8sSecretBackend = "K8S_SECRET_BACKEND"

	OperatorSecretPath    = "secret/data/mongodbenterprise/operator"
	DatabaseSecretPath    = "secret/data/mongodbenterprise/database"
	DatabaseVaultRoleName = "mongodbenterprisedatabase"
)

type SecretsToInject struct {
	AgentCerts  string
	AgentApiKey string

	InternalClusterAuth string
	InternalClusterHash string
}

func IsVaultSecretBackend() bool {
	return os.Getenv("SECRET_BACKEND") == VaultBackend
}

func VaultAddress() string {
	// TODO: The vault configurations would be specified in a configmap
	// read from there.
	return "http://vault.vault.svc.cluster.local:8200"
}

type VaultClient struct {
	client *api.Client
}

func GetVaultClient() (*VaultClient, error) {
	client, err := api.NewClient(&api.Config{Address: VaultAddress(), HttpClient: &http.Client{
		Timeout: 10 * time.Second,
	}})

	if err != nil {
		return nil, err
	}

	return &VaultClient{client: client}, nil
}

func (v *VaultClient) Login() error {
	// Read the service-account token from the path where the token's Kubernetes Secret is mounted.
	jwt, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return fmt.Errorf("unable to read file containing service account token: %w", err)
	}

	params := map[string]interface{}{
		"jwt":  string(jwt),
		"role": "mongodbenterprise", // the name of the role in Vault that was created with this app's Kubernetes service account bound to it
	}

	// log in to Vault's Kubernetes auth method
	resp, err := v.client.Logical().Write("auth/kubernetes/login", params)
	if err != nil {
		return fmt.Errorf("unable to log in with Kubernetes auth: %w", err)
	}

	if resp == nil || resp.Auth == nil || resp.Auth.ClientToken == "" {
		return fmt.Errorf("login response did not return client token")
	}

	// will use the resulting Vault token for making all future calls to Vault
	v.client.SetToken(resp.Auth.ClientToken)
	return nil
}

func (v *VaultClient) PutSecret(path string, data map[string]interface{}) error {
	if err := v.Login(); err != nil {
		return fmt.Errorf("unable to log in: %s", err)
	}
	_, err := v.client.Logical().Write(path, data)
	if err != nil {
		return err
	}
	return nil
}

func (v *VaultClient) ReadSecretBytes(path string) (map[string][]byte, error) {
	if err := v.Login(); err != nil {
		return map[string][]byte{}, fmt.Errorf("unable to log in: %s", err)
	}
	secret, err := v.client.Logical().Read(path)
	if err != nil {
		return map[string][]byte{}, fmt.Errorf("can't read secret from vault: %s", err)
	}
	if secret == nil {
		return map[string][]byte{}, fmt.Errorf("secret not found at %s", path)
	}
	secrets := make(map[string][]byte)
	for k, v := range maputil.ReadMapValueAsMap(secret.Data, "data") {
		secrets[k] = []byte(fmt.Sprintf("%v", v))
	}
	return secrets, nil
}

func (v *VaultClient) ReadSecretString(path string) (map[string]string, error) {
	secretBytes, err := v.ReadSecretBytes(path)
	if err != nil {
		return map[string]string{}, err
	}

	secretString := map[string]string{}
	for k, v := range secretBytes {
		secretString[k] = string(v)
	}
	return secretString, nil
}

func (s SecretsToInject) DatabaseAnnotations(namespace string) map[string]string {
	apiKeySecretPath := fmt.Sprintf("%s/%s", DatabaseSecretPath, s.AgentApiKey)

	agentAPIKeyTemplate := fmt.Sprintf(`{{- with secret "%s" -}}
          {{ .Data.data.agentApiKey }}
          {{- end }}`, apiKeySecretPath)

	annotations := map[string]string{
		"vault.hashicorp.com/agent-inject":                      "true",
		"vault.hashicorp.com/agent-inject-secret-agentApiKey":   apiKeySecretPath,
		"vault.hashicorp.com/role":                              DatabaseVaultRoleName,
		"vault.hashicorp.com/secret-volume-path-agentApiKey":    "/mongodb-automation/agent-api-key",
		"vault.hashicorp.com/preserve-secret-case":              "true",
		"vault.hashicorp.com/agent-inject-template-agentApiKey": agentAPIKeyTemplate,
	}
	if s.AgentCerts != "" {
		agentCertsPath := fmt.Sprintf("%s/%s/%s", DatabaseSecretPath, namespace, s.AgentCerts)
		annotations["vault.hashicorp.com/agent-inject-secret-mms-automation-agent-pem"] = agentCertsPath
		annotations["vault.hashicorp.com/secret-volume-path-mms-automation-agent-pem"] = "/mongodb-automation/agent-certs"
		annotations["vault.hashicorp.com/agent-inject-template-mms-automation-agent-pem"] = fmt.Sprintf(`{{- with secret "%s" -}}
          {{ range $k, $v := .Data.data }}
          {{- $v }}
          {{- end }}
          {{- end }}`, agentCertsPath)
	}
	if s.InternalClusterAuth != "" {
		internalClusterPath := fmt.Sprintf("%s/%s/%s", DatabaseSecretPath, namespace, s.InternalClusterAuth)

		annotations["vault.hashicorp.com/agent-inject-secret-internal-cluster"] = internalClusterPath
		annotations["vault.hashicorp.com/agent-inject-file-internal-cluster"] = s.InternalClusterHash
		annotations["vault.hashicorp.com/secret-volume-path-internal-cluster"] = util.InternalClusterAuthMountPath
		annotations["vault.hashicorp.com/agent-inject-template-internal-cluster"] = fmt.Sprintf(`{{- with secret "%s" -}}
          {{ range $k, $v := .Data.data }}
          {{- $v }}
          {{- end }}
          {{- end }}`, internalClusterPath)
	}
	return annotations
}
