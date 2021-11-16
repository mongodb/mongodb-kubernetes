package vault

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hashicorp/vault/api"
)

const (
	VaultBackend     = "VAULT_BACKEND"
	K8sSecretBackend = "K8S_SECRET_BACKEND"

	OperatorSecretPath = "secret/data/mongodbenterprise/operator/"
)

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

func (v *VaultClient) GetSecret(path string) (map[string]interface{}, error) {
	if err := v.Login(); err != nil {
		return map[string]interface{}{}, fmt.Errorf("unable to log in: %s", err)
	}
	secret, err := v.client.Logical().Read(path)
	if err != nil {
		return map[string]interface{}{}, fmt.Errorf("can't read secret from vault: %s", err)
	}
	if secret == nil {
		return map[string]interface{}{}, fmt.Errorf("secret not found at %s", path)
	}
	return secret.Data, nil
}
