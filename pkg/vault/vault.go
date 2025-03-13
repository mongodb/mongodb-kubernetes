package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/hashicorp/vault/api"
	"golang.org/x/xerrors"
	"k8s.io/client-go/kubernetes"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
)

const (
	VaultBackend     = "VAULT_BACKEND"
	K8sSecretBackend = "K8S_SECRET_BACKEND" //nolint

	DEFAULT_OPERATOR_SECRET_PATH    = "mongodbenterprise/operator"
	DEFAULT_OPS_MANAGER_SECRET_PATH = "mongodbenterprise/opsmanager"
	DEFAULT_DATABASE_SECRET_PATH    = "mongodbenterprise/database"
	DEFAULT_APPDB_SECRET_PATH       = "mongodbenterprise/appdb"

	DEFAULT_VAULT_ADDRESS = "vault.vault.svc.cluster.local"
	DEFAULT_VAULT_PORT    = "8200"

	DatabaseVaultRoleName   = "mongodbenterprisedatabase"
	OpsManagerVaultRoleName = "mongodbenterpriseopsmanager"
	AppDBVaultRoleName      = "mongodbenterpriseappdb"

	VAULT_SERVER_ADDRESS         = "VAULT_SERVER_ADDRESS"
	OPERATOR_SECRET_BASE_PATH    = "OPERATOR_SECRET_BASE_PATH"
	TLS_SECRET_REF               = "TLS_SECRET_REF"               //nolint
	OPS_MANAGER_SECRET_BASE_PATH = "OPS_MANAGER_SECRET_BASE_PATH" //nolint
	DATABASE_SECRET_BASE_PATH    = "DATABASE_SECRET_BASE_PATH"    //nolint
	APPDB_SECRET_BASE_PATH       = "APPDB_SECRET_BASE_PATH"       //nolint

	DEFAULT_AGENT_INJECT_TEMPLATE = `{{- with secret "%s" -}}
          {{ index .Data.data "%s" }}
          {{- end }}`
	PREVIOUS_HASH_INJECT_COMMAND  = `sh -c 'test -s %[1]s/%[2]s && tail -n+2 %[1]s/%[2]s > %[1]s/\$(head -n1 %[1]s/%[2]s) || true'`
	PREVIOUS_HASH_INJECT_TEMPLATE = `{{- with secret "%s" -}}
{{- if .Data.data.%[2]s -}}
{{ .Data.data.%[2]s }}
{{ index .Data.data (.Data.data.%[2]s) }}
{{- end }}
{{- end }}`
)

type DatabaseSecretsToInject struct {
	AgentCerts            string
	AgentApiKey           string
	InternalClusterAuth   string
	InternalClusterHash   string
	MemberClusterAuth     string
	MemberClusterHash     string
	Config                VaultConfiguration
	Prometheus            string
	PrometheusTLSCertHash string
}

type AppDBSecretsToInject struct {
	AgentApiKey    string
	TLSSecretName  string
	TLSClusterHash string

	AutomationConfigSecretName string
	AutomationConfigPath       string
	AgentType                  string
	Config                     VaultConfiguration

	PrometheusTLSCertHash string
	PrometheusTLSPath     string
}

type OpsManagerSecretsToInject struct {
	TLSSecretName         string
	TLSHash               string
	GenKeyPath            string
	AppDBConnection       string
	AppDBConnectionVolume string
	Config                VaultConfiguration
}

func IsVaultSecretBackend() bool {
	return os.Getenv("SECRET_BACKEND") == VaultBackend // nolint:forbidigo
}

type VaultConfiguration struct {
	OperatorSecretPath   string
	DatabaseSecretPath   string
	OpsManagerSecretPath string
	AppDBSecretPath      string
	VaultAddress         string
	TLSSecretRef         string
}

type VaultClient struct {
	client      *api.Client
	VaultConfig VaultConfiguration
}

func readVaultConfig(ctx context.Context, client *kubernetes.Clientset) VaultConfiguration {
	cm, err := client.CoreV1().ConfigMaps(env.ReadOrPanic(util.CurrentNamespace)).Get(ctx, "secret-configuration", v1.GetOptions{}) // nolint:forbidigo
	if err != nil {
		panic(xerrors.Errorf("error reading vault configmap: %w", err))
	}

	config := VaultConfiguration{
		OperatorSecretPath:   cm.Data[OPERATOR_SECRET_BASE_PATH],
		VaultAddress:         cm.Data[VAULT_SERVER_ADDRESS],
		OpsManagerSecretPath: cm.Data[OPS_MANAGER_SECRET_BASE_PATH],
		DatabaseSecretPath:   cm.Data[DATABASE_SECRET_BASE_PATH],
		AppDBSecretPath:      cm.Data[APPDB_SECRET_BASE_PATH],
	}

	if tlsRef, ok := cm.Data[TLS_SECRET_REF]; ok {
		config.TLSSecretRef = tlsRef
	}

	return config
}

func setTLSConfig(ctx context.Context, config *api.Config, client *kubernetes.Clientset, tlsSecretRef string) error {
	if tlsSecretRef == "" {
		return nil
	}
	var secret *corev1.Secret
	var err error
	secret, err = client.CoreV1().Secrets(env.ReadOrPanic(util.CurrentNamespace)).Get(ctx, tlsSecretRef, v1.GetOptions{}) // nolint:forbidigo
	if err != nil {
		return xerrors.Errorf("can't read tls secret %s for vault: %w", tlsSecretRef, err)
	}

	// Read the secret and write ca.crt to a temporary file
	caData := secret.Data["ca.crt"]
	f, err := os.CreateTemp("/tmp", "VaultCAData")
	if err != nil {
		return xerrors.Errorf("can't create temporary file for CA data: %w", err)
	}
	defer f.Close()

	_, err = f.Write(caData)
	if err != nil {
		return xerrors.Errorf("can't write caData to file %s: %w", f.Name(), err)
	}
	if err = f.Sync(); err != nil {
		return xerrors.Errorf("can't call Sync on file %s: %w", f.Name(), err)
	}

	return config.ConfigureTLS(
		&api.TLSConfig{
			CACert: f.Name(),
		},
	)
}

func InitVaultClient(ctx context.Context, client *kubernetes.Clientset) (*VaultClient, error) {
	vaultConfig := readVaultConfig(ctx, client)

	config := api.DefaultConfig()
	config.Address = vaultConfig.VaultAddress

	if err := setTLSConfig(ctx, config, client, vaultConfig.TLSSecretRef); err != nil {
		return nil, err
	}

	vclient, err := api.NewClient(config)
	if err != nil {
		return nil, err
	}

	return &VaultClient{client: vclient, VaultConfig: vaultConfig}, nil
}

func (v *VaultClient) Login() error {
	// Read the service-account token from the path where the token's Kubernetes Secret is mounted.
	jwt, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return xerrors.Errorf("unable to read file containing service account token: %w", err)
	}

	params := map[string]interface{}{
		"jwt":  string(jwt),
		"role": "mongodbenterprise", // the name of the role in Vault that was created with this app's Kubernetes service account bound to it
	}

	// log in to Vault's Kubernetes auth method
	resp, err := v.client.Logical().Write("auth/kubernetes/login", params)
	if err != nil {
		return xerrors.Errorf("unable to log in with Kubernetes auth: %w", err)
	}

	if resp == nil || resp.Auth == nil || resp.Auth.ClientToken == "" {
		return xerrors.Errorf("login response did not return client token")
	}

	// will use the resulting Vault token for making all future calls to Vault
	v.client.SetToken(resp.Auth.ClientToken)
	return nil
}

func (v *VaultClient) PutSecret(path string, data map[string]interface{}) error {
	if err := v.Login(); err != nil {
		return xerrors.Errorf("unable to log in: %w", err)
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
		return nil, xerrors.Errorf("unable to log in: %w", err)
	}
	secret, err := v.client.Logical().Read(path)
	if err != nil {
		return nil, xerrors.Errorf("can't read secret from vault: %w", err)
	}
	if secret == nil {
		return nil, xerrors.Errorf("secret not found at %s", path)
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

func (v VaultConfiguration) TLSAnnotations() map[string]string {
	if v.TLSSecretRef == "" {
		return map[string]string{}
	}
	return map[string]string{
		"vault.hashicorp.com/tls-secret": v.TLSSecretRef,
		"vault.hashicorp.com/ca-cert":    "/vault/tls/ca.crt",
	}
}

func (v *VaultClient) OperatorSecretPath() string {
	if v.VaultConfig.OperatorSecretPath != "" {
		return fmt.Sprintf("/secret/data/%s", v.VaultConfig.OperatorSecretPath)
	}
	return fmt.Sprintf("/secret/data/%s", DEFAULT_OPERATOR_SECRET_PATH)
}

func (v *VaultClient) OperatorScretMetadataPath() string {
	if v.VaultConfig.OperatorSecretPath != "" {
		return fmt.Sprintf("/secret/metadata/%s", v.VaultConfig.OperatorSecretPath)
	}
	return fmt.Sprintf("/secret/metadata/%s", DEFAULT_OPERATOR_SECRET_PATH)
}

func (v *VaultClient) OpsManagerSecretPath() string {
	if v.VaultConfig.OperatorSecretPath != "" {
		return fmt.Sprintf("/secret/data/%s", v.VaultConfig.OpsManagerSecretPath)
	}
	return fmt.Sprintf("/secret/data/%s", DEFAULT_OPS_MANAGER_SECRET_PATH)
}

func (v *VaultClient) OpsManagerSecretMetadataPath() string {
	if v.VaultConfig.OpsManagerSecretPath != "" {
		return fmt.Sprintf("/secret/metadata/%s", v.VaultConfig.OpsManagerSecretPath)
	}
	return fmt.Sprintf("/secret/metadata/%s", DEFAULT_OPS_MANAGER_SECRET_PATH)
}

func (v *VaultClient) DatabaseSecretPath() string {
	if v.VaultConfig.OperatorSecretPath != "" {
		return fmt.Sprintf("/secret/data/%s", v.VaultConfig.DatabaseSecretPath)
	}
	return fmt.Sprintf("/secret/data/%s", DEFAULT_DATABASE_SECRET_PATH)
}

func (v *VaultClient) DatabaseSecretMetadataPath() string {
	if v.VaultConfig.OperatorSecretPath != "" {
		return fmt.Sprintf("/secret/metadata/%s", v.VaultConfig.DatabaseSecretPath)
	}
	return fmt.Sprintf("/secret/metadata/%s", DEFAULT_DATABASE_SECRET_PATH)
}

func (v *VaultClient) AppDBSecretPath() string {
	if v.VaultConfig.AppDBSecretPath != "" {
		return fmt.Sprintf("/secret/data/%s", v.VaultConfig.AppDBSecretPath)
	}
	return fmt.Sprintf("/secret/data/%s", APPDB_SECRET_BASE_PATH)
}

func (v *VaultClient) AppDBSecretMetadataPath() string {
	if v.VaultConfig.AppDBSecretPath != "" {
		return fmt.Sprintf("/secret/metadata/%s", v.VaultConfig.AppDBSecretPath)
	}
	return fmt.Sprintf("/secret/metadata/%s", APPDB_SECRET_BASE_PATH)
}

func (s OpsManagerSecretsToInject) OpsManagerAnnotations(namespace string) map[string]string {
	var opsManagerSecretPath string
	if s.Config.OpsManagerSecretPath != "" {
		opsManagerSecretPath = fmt.Sprintf("/secret/data/%s", s.Config.OpsManagerSecretPath)
	} else {
		opsManagerSecretPath = fmt.Sprintf("/secret/metadata/%s", DEFAULT_OPS_MANAGER_SECRET_PATH)
	}

	annotations := map[string]string{
		"vault.hashicorp.com/agent-inject":         "true",
		"vault.hashicorp.com/role":                 OpsManagerVaultRoleName,
		"vault.hashicorp.com/preserve-secret-case": "true",
	}

	annotations = merge.StringToStringMap(annotations, s.Config.TLSAnnotations())

	if s.TLSSecretName != "" {
		omTLSPath := fmt.Sprintf("%s/%s/%s", opsManagerSecretPath, namespace, s.TLSSecretName)
		annotations["vault.hashicorp.com/agent-inject-secret-om-tls-cert-pem"] = omTLSPath
		annotations["vault.hashicorp.com/agent-inject-file-om-tls-cert-pem"] = s.TLSHash
		annotations["vault.hashicorp.com/secret-volume-path-om-tls-cert-pem"] = util.MmsPemKeyFileDirInContainer
		annotations["vault.hashicorp.com/agent-inject-template-om-tls-cert-pem"] = fmt.Sprintf(
			DEFAULT_AGENT_INJECT_TEMPLATE, omTLSPath, s.TLSHash)

		annotations["vault.hashicorp.com/agent-inject-secret-previous-om-tls-cert-pem"] = omTLSPath
		annotations["vault.hashicorp.com/secret-volume-path-previous-om-tls-cert-pem"] = util.MmsPemKeyFileDirInContainer
		annotations["vault.hashicorp.com/agent-inject-file-previous-om-tls-cert-pem"] = util.PreviousHashSecretKey
		annotations["vault.hashicorp.com/agent-inject-template-previous-om-tls-cert-pem"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_TEMPLATE, omTLSPath, util.PreviousHashSecretKey)
		annotations["vault.hashicorp.com/agent-inject-command-previous-om-tls-cert-pem"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_COMMAND, util.MmsPemKeyFileDirInContainer, util.PreviousHashSecretKey)
	}

	if s.GenKeyPath != "" {
		genKeyPath := fmt.Sprintf("%s/%s/%s", opsManagerSecretPath, namespace, s.GenKeyPath)
		annotations["vault.hashicorp.com/agent-inject-secret-gen-key"] = genKeyPath
		annotations["vault.hashicorp.com/agent-inject-file-gen-key"] = "gen.key"
		annotations["vault.hashicorp.com/secret-volume-path-gen-key"] = util.GenKeyPath
		annotations["vault.hashicorp.com/agent-inject-template-gen-key"] = fmt.Sprintf(`{{- with secret "%s" -}}
          {{ range $k, $v := .Data.data }}
          {{- base64Decode $v }}
          {{- end }}
          {{- end }}`, genKeyPath)
	}

	// add appDB connection string
	appDBConnPath := fmt.Sprintf("%s/%s/%s", opsManagerSecretPath, namespace, s.AppDBConnection)
	annotations["vault.hashicorp.com/agent-inject-secret-appdb-connection-string"] = appDBConnPath
	annotations["vault.hashicorp.com/agent-inject-file-appdb-connection-string"] = util.AppDbConnectionStringKey
	annotations["vault.hashicorp.com/secret-volume-path-appdb-connection-string"] = s.AppDBConnectionVolume
	annotations["vault.hashicorp.com/agent-inject-template-appdb-connection-string"] = fmt.Sprintf(
		DEFAULT_AGENT_INJECT_TEMPLATE, appDBConnPath, util.AppDbConnectionStringKey)
	return annotations
}

func (s DatabaseSecretsToInject) DatabaseAnnotations(namespace string) map[string]string {
	var databaseSecretPath string
	if s.Config.DatabaseSecretPath != "" {
		databaseSecretPath = fmt.Sprintf("/secret/data/%s", s.Config.DatabaseSecretPath)
	} else {
		databaseSecretPath = fmt.Sprintf("/secret/data/%s", DEFAULT_DATABASE_SECRET_PATH)
	}

	apiKeySecretPath := fmt.Sprintf("%s/%s/%s", databaseSecretPath, namespace, s.AgentApiKey)

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

	annotations = merge.StringToStringMap(annotations, s.Config.TLSAnnotations())

	if s.AgentCerts != "" {
		agentCertsPath := fmt.Sprintf("%s/%s/%s", databaseSecretPath, namespace, s.AgentCerts)
		annotations["vault.hashicorp.com/agent-inject-secret-mms-automation-agent-pem"] = agentCertsPath
		annotations["vault.hashicorp.com/secret-volume-path-mms-automation-agent-pem"] = "/mongodb-automation/agent-certs"
		annotations["vault.hashicorp.com/agent-inject-template-mms-automation-agent-pem"] = fmt.Sprintf(
			DEFAULT_AGENT_INJECT_TEMPLATE, agentCertsPath, util.AutomationAgentPemSecretKey)
	}
	if s.InternalClusterAuth != "" {
		internalClusterPath := fmt.Sprintf("%s/%s/%s", databaseSecretPath, namespace, s.InternalClusterAuth)

		annotations["vault.hashicorp.com/agent-inject-secret-internal-cluster"] = internalClusterPath
		annotations["vault.hashicorp.com/agent-inject-file-internal-cluster"] = s.InternalClusterHash
		annotations["vault.hashicorp.com/secret-volume-path-internal-cluster"] = util.InternalClusterAuthMountPath
		annotations["vault.hashicorp.com/agent-inject-template-internal-cluster"] = fmt.Sprintf(
			DEFAULT_AGENT_INJECT_TEMPLATE, internalClusterPath, s.InternalClusterHash)

		annotations["vault.hashicorp.com/agent-inject-secret-previous-internal-cluster"] = internalClusterPath
		annotations["vault.hashicorp.com/secret-volume-path-previous-internal-cluster"] = util.InternalClusterAuthMountPath
		annotations["vault.hashicorp.com/agent-inject-file-previous-internal-cluster"] = util.PreviousHashSecretKey
		annotations["vault.hashicorp.com/agent-inject-template-previous-internal-cluster"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_TEMPLATE, internalClusterPath, util.PreviousHashSecretKey)
		annotations["vault.hashicorp.com/agent-inject-command-previous-internal-cluster"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_COMMAND, util.InternalClusterAuthMountPath, util.PreviousHashSecretKey)
	}
	if s.MemberClusterAuth != "" {
		memberClusterPath := fmt.Sprintf("%s/%s/%s", databaseSecretPath, namespace, s.MemberClusterAuth)

		annotations["vault.hashicorp.com/agent-inject-secret-tls-certificate"] = memberClusterPath
		annotations["vault.hashicorp.com/agent-inject-file-tls-certificate"] = s.MemberClusterHash
		annotations["vault.hashicorp.com/secret-volume-path-tls-certificate"] = util.TLSCertMountPath
		annotations["vault.hashicorp.com/agent-inject-template-tls-certificate"] = fmt.Sprintf(
			DEFAULT_AGENT_INJECT_TEMPLATE, memberClusterPath, s.MemberClusterHash)

		annotations["vault.hashicorp.com/agent-inject-secret-previous-tls-certificate"] = memberClusterPath
		annotations["vault.hashicorp.com/secret-volume-path-previous-tls-certificate"] = util.TLSCertMountPath
		annotations["vault.hashicorp.com/agent-inject-file-previous-tls-certificate"] = util.PreviousHashSecretKey
		annotations["vault.hashicorp.com/agent-inject-template-previous-tls-certificate"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_TEMPLATE, memberClusterPath, util.PreviousHashSecretKey)
		annotations["vault.hashicorp.com/agent-inject-command-previous-tls-certificate"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_COMMAND, util.TLSCertMountPath, util.PreviousHashSecretKey)
	}

	if s.Prometheus != "" {
		promPath := fmt.Sprintf("%s/%s/%s", databaseSecretPath, namespace, s.Prometheus)

		annotations["vault.hashicorp.com/agent-inject-secret-prom-https-cert"] = promPath
		annotations["vault.hashicorp.com/agent-inject-file-prom-https-cert"] = s.PrometheusTLSCertHash
		annotations["vault.hashicorp.com/secret-volume-path-prom-https-cert"] = util.SecretVolumeMountPathPrometheus
		annotations["vault.hashicorp.com/agent-inject-template-prom-https-cert"] = fmt.Sprintf(
			DEFAULT_AGENT_INJECT_TEMPLATE, promPath, s.PrometheusTLSCertHash)

		annotations["vault.hashicorp.com/agent-inject-secret-previous-prom-https-cert"] = promPath
		annotations["vault.hashicorp.com/secret-volume-path-previous-prom-https-cert"] = util.SecretVolumeMountPathPrometheus
		annotations["vault.hashicorp.com/agent-inject-file-previous-prom-https-cert"] = util.PreviousHashSecretKey
		annotations["vault.hashicorp.com/agent-inject-template-previous-prom-https-cert"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_TEMPLATE, promPath, util.PreviousHashSecretKey)
		annotations["vault.hashicorp.com/agent-inject-command-previous-prom-https-cert"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_COMMAND, util.SecretVolumeMountPathPrometheus, util.PreviousHashSecretKey)
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

	annotations = merge.StringToStringMap(annotations, a.Config.TLSAnnotations())
	var appdbSecretPath string
	if a.Config.AppDBSecretPath != "" {
		appdbSecretPath = fmt.Sprintf("/secret/data/%s", a.Config.AppDBSecretPath)
	} else {
		appdbSecretPath = fmt.Sprintf("/secret/data/%s", APPDB_SECRET_BASE_PATH)
	}
	if a.AgentApiKey != "" {

		apiKeySecretPath := fmt.Sprintf("%s/%s/%s", appdbSecretPath, namespace, a.AgentApiKey)
		agentAPIKeyTemplate := fmt.Sprintf(DEFAULT_AGENT_INJECT_TEMPLATE, apiKeySecretPath, util.OmAgentApiKey)

		annotations["vault.hashicorp.com/agent-inject-secret-agentApiKey"] = apiKeySecretPath
		annotations["vault.hashicorp.com/secret-volume-path-agentApiKey"] = "/mongodb-automation/agent-api-key"
		annotations["vault.hashicorp.com/agent-inject-template-agentApiKey"] = agentAPIKeyTemplate
	}

	if a.TLSSecretName != "" {
		memberClusterPath := fmt.Sprintf("%s/%s/%s", appdbSecretPath, namespace, a.TLSSecretName)
		annotations["vault.hashicorp.com/agent-inject-secret-tls-certificate"] = memberClusterPath
		annotations["vault.hashicorp.com/agent-inject-file-tls-certificate"] = a.TLSClusterHash
		annotations["vault.hashicorp.com/secret-volume-path-tls-certificate"] = util.SecretVolumeMountPath + "/certs"
		annotations["vault.hashicorp.com/agent-inject-template-tls-certificate"] = fmt.Sprintf(
			DEFAULT_AGENT_INJECT_TEMPLATE, memberClusterPath, a.TLSClusterHash)

		annotations["vault.hashicorp.com/agent-inject-secret-previous-tls-certificate"] = memberClusterPath
		annotations["vault.hashicorp.com/secret-volume-path-previous-tls-certificate"] = util.SecretVolumeMountPath + "/certs"
		annotations["vault.hashicorp.com/agent-inject-file-previous-tls-certificate"] = util.PreviousHashSecretKey
		annotations["vault.hashicorp.com/agent-inject-template-previous-tls-certificate"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_TEMPLATE, memberClusterPath, util.PreviousHashSecretKey)
		annotations["vault.hashicorp.com/agent-inject-command-previous-tls-certificate"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_COMMAND, util.SecretVolumeMountPath+"/certs", util.PreviousHashSecretKey)

	}

	if a.AutomationConfigSecretName != "" {
		// There are two different type of annotations here: for the automation agent
		// and for the monitoring agent.
		acSecretPath := fmt.Sprintf("%s/%s/%s", appdbSecretPath, namespace, a.AutomationConfigSecretName)
		annotations["vault.hashicorp.com/agent-inject-secret-"+a.AgentType] = acSecretPath
		annotations["vault.hashicorp.com/agent-inject-file-"+a.AgentType] = a.AutomationConfigPath
		annotations["vault.hashicorp.com/secret-volume-path-"+a.AgentType] = "/var/lib/automation/config"
		annotations["vault.hashicorp.com/agent-inject-template-"+a.AgentType] = fmt.Sprintf(`{{- with secret "%s" -}}
          {{ range $k, $v := .Data.data }}
          {{- $v }}
          {{- end }}
          {{- end }}`, acSecretPath)
	}

	if a.PrometheusTLSCertHash != "" && a.PrometheusTLSPath != "" {
		promPath := fmt.Sprintf("%s/%s/%s", appdbSecretPath, namespace, a.PrometheusTLSPath)
		annotations["vault.hashicorp.com/agent-inject-secret-prom-https-cert"] = promPath
		annotations["vault.hashicorp.com/agent-inject-file-prom-https-cert"] = a.PrometheusTLSCertHash
		annotations["vault.hashicorp.com/secret-volume-path-prom-https-cert"] = util.SecretVolumeMountPathPrometheus
		annotations["vault.hashicorp.com/agent-inject-template-prom-https-cert"] = fmt.Sprintf(
			DEFAULT_AGENT_INJECT_TEMPLATE, promPath, a.PrometheusTLSCertHash)

		annotations["vault.hashicorp.com/agent-inject-secret-previous-prom-https-cert"] = promPath
		annotations["vault.hashicorp.com/secret-volume-path-previous-prom-https-cert"] = util.SecretVolumeMountPathPrometheus
		annotations["vault.hashicorp.com/agent-inject-file-previous-prom-https-cert"] = util.PreviousHashSecretKey
		annotations["vault.hashicorp.com/agent-inject-template-previous-prom-https-cert"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_TEMPLATE, promPath, util.PreviousHashSecretKey)
		annotations["vault.hashicorp.com/agent-inject-command-previous-prom-https-cert"] = fmt.Sprintf(
			PREVIOUS_HASH_INJECT_COMMAND, util.SecretVolumeMountPathPrometheus, util.PreviousHashSecretKey)
	}

	return annotations
}
