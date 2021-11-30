package vault

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
	"github.com/hashicorp/vault/api"
)

const (
	VaultBackend     = "VAULT_BACKEND"
	K8sSecretBackend = "K8S_SECRET_BACKEND"

	OperatorSecretPath   = "secret/data/mongodbenterprise/operator"
	DatabaseSecretPath   = "secret/data/mongodbenterprise/database"
	OpsManagerSecretPath = "secret/data/mongodbenterprise/opsmanager"
	AppDBSecretPath      = "secret/data/mongodbenterprise/appdb"

	DatabaseVaultRoleName   = "mongodbenterprisedatabase"
	OpsManagerVaultRoleName = "mongodbenterpriseopsmanager"
	AppDBVaultRoleName      = "mongodbenterpriseappdb"

	OperatorSecretMetadataPath = "secret/metadata/mongodbenterprise/operator"
	DatabaseSecretMetadataPath = "secret/metadata/mongodbenterprise/database"
)

type DatabaseSecretsToInject struct {
	AgentCerts  string
	AgentApiKey string

	InternalClusterAuth string
	InternalClusterHash string

	MemberClusterAuth string
	MemberClusterHash string
}

type AppDBSecretsToInject struct {
	AgentApiKey string

	TLSSecretName  string
	TLSClusterHash string
}

type OpsManagerSecretsToInject struct {
	OpsManagerTLSSecretName string
	OpsManagerTLSHash       string

	GenKeyPath string
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

func (v *VaultClient) ReadSecretVersion(path string) (int, error) {
	data, err := v.ReadSecret(path)
	if err != nil {
		return -1, err
	}
	current_version, err := data.Data["current_version"].(json.Number).Int64()
	if err != nil {
		// this shouldn't happen but if it does the caller should log it with error that
		// secret rotation won't work
		return -1, err
	}
	return int(current_version), nil
}

func (v *VaultClient) ReadSecret(path string) (*api.Secret, error) {
	if err := v.Login(); err != nil {
		return nil, fmt.Errorf("unable to log in: %s", err)
	}
	secret, err := v.client.Logical().Read(path)
	if err != nil {
		return nil, fmt.Errorf("can't read secret from vault: %s", err)
	}
	if secret == nil {
		return nil, fmt.Errorf("secret not found at %s", path)
	}
	return secret, nil
}

func (v *VaultClient) ReadSecretBytes(path string) (map[string][]byte, error) {
	secret, err := v.ReadSecret(path)
	if err != nil {
		return map[string][]byte{}, err
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

func (s OpsManagerSecretsToInject) OpsManagerAnnotations(namespace string) map[string]string {
	annotations := map[string]string{
		"vault.hashicorp.com/agent-inject":         "true",
		"vault.hashicorp.com/role":                 OpsManagerVaultRoleName,
		"vault.hashicorp.com/preserve-secret-case": "true",
	}

	if s.OpsManagerTLSSecretName != "" {
		omTLSPath := fmt.Sprintf("%s/%s/%s", OpsManagerSecretPath, namespace, s.OpsManagerTLSSecretName)
		annotations["vault.hashicorp.com/agent-inject-secret-om-tls-cert-pem"] = omTLSPath
		annotations["vault.hashicorp.com/agent-inject-file-om-tls-cert-pem"] = s.OpsManagerTLSHash
		annotations["vault.hashicorp.com/secret-volume-path-om-tls-cert-pem"] = util.MmsPemKeyFileDirInContainer
		annotations["vault.hashicorp.com/agent-inject-template-om-tls-cert-pem"] = fmt.Sprintf(`{{- with secret "%s" -}}
          {{ range $k, $v := .Data.data }}
          {{- $v }}
          {{- end }}
          {{- end }}`, omTLSPath)
	}

	if s.GenKeyPath != "" {
		genKeyPath := fmt.Sprintf("%s/%s/%s", OpsManagerSecretPath, namespace, s.GenKeyPath)
		annotations["vault.hashicorp.com/agent-inject-secret-gen-key"] = genKeyPath
		annotations["vault.hashicorp.com/agent-inject-file-gen-key"] = "gen.key"
		annotations["vault.hashicorp.com/secret-volume-path-gen-key"] = util.GenKeyPath
		annotations["vault.hashicorp.com/agent-inject-template-gen-key"] = fmt.Sprintf(`{{- with secret "%s" -}}
          {{ range $k, $v := .Data.data }}
          {{- base64Decode $v }}
          {{- end }}
          {{- end }}`, genKeyPath)
	}
	return annotations
}

func (s DatabaseSecretsToInject) DatabaseAnnotations(namespace string) map[string]string {
	apiKeySecretPath := fmt.Sprintf("%s/%s/%s", DatabaseSecretPath, namespace, s.AgentApiKey)

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
	if s.MemberClusterAuth != "" {
		memberClusterPath := fmt.Sprintf("%s/%s/%s", DatabaseSecretPath, namespace, s.InternalClusterAuth)

		annotations["vault.hashicorp.com/agent-inject-secret-tls-certificate"] = memberClusterPath
		annotations["vault.hashicorp.com/agent-inject-file-tls-certificate"] = s.MemberClusterHash
		annotations["vault.hashicorp.com/secret-volume-path-tls-certificate"] = util.TLSCertMountPath
		annotations["vault.hashicorp.com/agent-inject-template-tls-certificate"] = fmt.Sprintf(`{{- with secret "%s" -}}
          {{ range $k, $v := .Data.data }}
          {{- $v }}
          {{- end }}
          {{- end }}`, memberClusterPath)
	}
	if s.MemberClusterAuth != "" {

	}
	return annotations
}

func (v *VaultClient) GetSecretAnnotation(path string) map[string]string {
	n, err := v.ReadSecretVersion(path)
	if err != nil {
		return map[string]string{}
	}

	ss := strings.Split(path, "/")
	secretName := ss[len(ss)-1]

	return map[string]string{
		secretName: strconv.FormatInt(int64(n), 10),
	}
}

func (a AppDBSecretsToInject) AppDBAnnotations(namespace string) map[string]string {

	annotations := map[string]string{
		"vault.hashicorp.com/agent-inject":         "true",
		"vault.hashicorp.com/role":                 AppDBVaultRoleName,
		"vault.hashicorp.com/preserve-secret-case": "true",
	}

	if a.AgentApiKey != "" {

		apiKeySecretPath := fmt.Sprintf("%s/%s/%s", AppDBSecretPath, namespace, a.AgentApiKey)
		agentAPIKeyTemplate := fmt.Sprintf(`{{- with secret "%s" -}}
          {{ .Data.data.agentApiKey }}
          {{- end }}`, apiKeySecretPath)

		annotations["vault.hashicorp.com/agent-inject-secret-agentApiKey"] = apiKeySecretPath
		annotations["vault.hashicorp.com/secret-volume-path-agentApiKey"] = "/mongodb-automation/agent-api-key"
		annotations["vault.hashicorp.com/agent-inject-template-agentApiKey"] = agentAPIKeyTemplate
	}

	if a.TLSSecretName != "" {
		memberClusterPath := fmt.Sprintf("%s/%s/%s", AppDBSecretPath, namespace, a.TLSSecretName)
		annotations["vault.hashicorp.com/agent-inject-secret-tls-certificate"] = memberClusterPath
		annotations["vault.hashicorp.com/agent-inject-file-tls-certificate"] = a.TLSClusterHash
		annotations["vault.hashicorp.com/secret-volume-path-tls-certificate"] = util.SecretVolumeMountPath + "/certs"
		annotations["vault.hashicorp.com/agent-inject-template-tls-certificate"] = fmt.Sprintf(`{{- with secret "%s" -}}
          {{ range $k, $v := .Data.data }}
          {{- $v }}
          {{- end }}
          {{- end }}`, memberClusterPath)

	}
	return annotations
}
