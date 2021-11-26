package secrets

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

type SecretClientInterface interface {
	ReadSecret(secretName types.NamespacedName, basePath string) (map[string]string, error)
}

var _ SecretClientInterface = (*SecretClient)(nil)

type SecretClient struct {
	VaultClient *vault.VaultClient
	KubeClient  kubernetesClient.KubernetesSecretClient
}

func namespacedNameToVaultPath(nsName types.NamespacedName, basePath string) string {
	return fmt.Sprintf("%s/%s/%s", basePath, nsName.Namespace, nsName.Name)
}

func secretNamespacedName(s corev1.Secret) types.NamespacedName {
	return types.NamespacedName{
		Namespace: s.Namespace,
		Name:      s.Name,
	}
}

func (r SecretClient) ReadSecretKey(secretName types.NamespacedName, basePath string, key string) (string, error) {
	secret, err := r.ReadSecret(secretName, basePath)
	if err != nil {
		return "", fmt.Errorf("can't read secret %s: %s", secretName, err)
	}
	val, ok := secret[key]
	if !ok {
		return "", fmt.Errorf("secret %s does not contain key %s", secretName, key)
	}
	return val, nil
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
		secretPath := namespacedNameToVaultPath(secretNamespacedName(s), basePath)
		secretData := map[string]interface{}{}
		for k, v := range s.StringData {
			secretData[k] = v
		}
		for k, v := range s.Data {
			secretData[k] = string(v)
		}
		data := map[string]interface{}{
			"data": secretData,
		}
		return r.VaultClient.PutSecret(secretPath, data)
	}

	return secret.CreateOrUpdate(r.KubeClient, s)

}

func (r SecretClient) PutSecretIfChanged(s corev1.Secret, basePath string) error {
	if vault.IsVaultSecretBackend() {
		secret, err := r.ReadSecret(secretNamespacedName(s), basePath)
		if err != nil && !strings.Contains(err.Error(), "not found") {
			return err
		}
		if err != nil || !reflect.DeepEqual(secret, s.StringData) {
			return r.PutSecret(s, basePath)
		}

	}
	return secret.CreateOrUpdateIfNeeded(r.KubeClient, s)
}

func SecretNotExist(err error) bool {
	if err == nil {
		return false
	}
	return apiErrors.IsNotFound(err) || strings.Contains(err.Error(), "secret not found")
}
