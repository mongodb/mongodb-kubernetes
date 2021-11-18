package secrets

import (
	"fmt"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type SecretClient struct {
	VaultClient *vault.VaultClient
	KubeClient  kubernetesClient.KubernetesSecretClient
}

func namespacedNameToVaultPath(nsName types.NamespacedName, basePath string) string {
	return fmt.Sprintf("%s/%s/%s", basePath, nsName.Namespace, nsName.Name)
}

func (r SecretClient) ReadSecret(secretName types.NamespacedName, basePath string) (map[string]string, error) {
	secrets := make(map[string]string)
	if vault.IsVaultSecretBackend() {
		var err error
		secretPath := namespacedNameToVaultPath(secretName, basePath)
		secrets, err = r.VaultClient.ReadSecretString(secretPath)
		if err != nil {
			return nil, err
		}
	} else {
		stringData, err := secret.ReadStringData(r.KubeClient, secretName)
		if err != nil {
			return nil, err
		}
		for k, v := range stringData {
			secrets[k] = strings.TrimSuffix(string(v[:]), "\n")
		}
	}
	return secrets, nil
}

func (r SecretClient) PutSecret(s corev1.Secret, basePath string) error {
	if vault.IsVaultSecretBackend() {
		secretPath := namespacedNameToVaultPath(kube.ObjectKey(s.Namespace, s.Name), basePath)
		stringsAsInterface := map[string]interface{}{}
		for k, v := range s.StringData {
			stringsAsInterface[k] = v
		}
		return r.VaultClient.PutSecret(secretPath, stringsAsInterface)
	}

	return secret.CreateOrUpdate(r.KubeClient, s)

}
