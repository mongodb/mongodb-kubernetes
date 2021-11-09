package vault

import (
	"net/http"
	"os"
	"time"

	"github.com/hashicorp/vault/api"
)

const (
	VaultBackend     = "VAULT_BACKEND"
	K8sSecretBackend = "K8S_SECRET_BACKEND"
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

func (v *VaultClient) PutSecret(path string, data map[string]interface{}) error {
	_, err := v.client.Logical().Write(path, data)
	if err != nil {
		return err
	}
	return nil
}
