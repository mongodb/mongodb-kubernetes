package secrets

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
)

type SecretClientInterface interface {
	ReadSecret(ctx context.Context, secretName types.NamespacedName, basePath string) (map[string]string, error)
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

func (r SecretClient) ReadSecretKey(ctx context.Context, secretName types.NamespacedName, basePath string, key string) (string, error) {
	secret, err := r.ReadSecret(ctx, secretName, basePath)
	if err != nil {
		return "", xerrors.Errorf("can't read secret %s: %w", secretName, err)
	}
	val, ok := secret[key]
	if !ok {
		return "", xerrors.Errorf("secret %s does not contain key %s", secretName, key)
	}
	return val, nil
}

func (r SecretClient) ReadSecret(ctx context.Context, secretName types.NamespacedName, basePath string) (map[string]string, error) {
	secrets := make(map[string]string)
	if vault.IsVaultSecretBackend() {
		var err error
		secretPath := namespacedNameToVaultPath(secretName, basePath)
		secrets, err = r.VaultClient.ReadSecretString(secretPath)
		if err != nil {
			return nil, err
		}
	} else {
		stringData, err := secret.ReadStringData(ctx, r.KubeClient, secretName)
		if err != nil {
			return nil, err
		}
		for k, v := range stringData {
			secrets[k] = strings.TrimSuffix(v[:], "\n")
		}
	}
	return secrets, nil
}

func (r SecretClient) ReadBinarySecret(ctx context.Context, secretName types.NamespacedName, basePath string) (map[string][]byte, error) {
	var secrets map[string][]byte
	var err error
	if vault.IsVaultSecretBackend() {
		secretPath := namespacedNameToVaultPath(secretName, basePath)
		secrets, err = r.VaultClient.ReadSecretBytes(secretPath)
		if err != nil {
			return nil, err
		}
	} else {
		secrets, err = secret.ReadByteData(ctx, r.KubeClient, secretName)
		if err != nil {
			return nil, err
		}
	}
	return secrets, nil
}

// PutSecret copies secret.Data into vault. Note: we don't rely on secret.StringData since our builder does not use the field.
func (r SecretClient) PutSecret(ctx context.Context, s corev1.Secret, basePath string) error {
	if vault.IsVaultSecretBackend() {
		secretPath := namespacedNameToVaultPath(secretNamespacedName(s), basePath)
		secretData := map[string]interface{}{}
		for k, v := range s.Data {
			secretData[k] = string(v)
		}
		data := map[string]interface{}{
			"data": secretData,
		}
		return r.VaultClient.PutSecret(secretPath, data)
	}

	return secret.CreateOrUpdate(ctx, r.KubeClient, s)
}

// PutBinarySecret copies secret.Data as base64 into vault.
func (r SecretClient) PutBinarySecret(ctx context.Context, s corev1.Secret, basePath string) error {
	if vault.IsVaultSecretBackend() {
		secretPath := namespacedNameToVaultPath(secretNamespacedName(s), basePath)
		secretData := map[string]interface{}{}
		for k, v := range s.Data {
			secretData[k] = base64.StdEncoding.EncodeToString(v)
		}
		data := map[string]interface{}{
			"data": secretData,
		}
		return r.VaultClient.PutSecret(secretPath, data)
	}

	return secret.CreateOrUpdate(ctx, r.KubeClient, s)
}

// PutSecretIfChanged updates a Secret only if it has changed. Equality is based on s.Data.
// `basePath` is only used when Secrets backend is `Vault`.
func (r SecretClient) PutSecretIfChanged(ctx context.Context, s corev1.Secret, basePath string) error {
	if vault.IsVaultSecretBackend() {
		secret, err := r.ReadSecret(ctx, secretNamespacedName(s), basePath)
		if err != nil && !strings.Contains(err.Error(), "not found") {
			return err
		}
		if err != nil || !reflect.DeepEqual(secret, DataToStringData(s.Data)) {
			return r.PutSecret(ctx, s, basePath)
		}
	}

	return secret.CreateOrUpdateIfNeeded(ctx, r.KubeClient, s)
}

func SecretNotExist(err error) bool {
	if err == nil {
		return false
	}
	return apiErrors.IsNotFound(err) || strings.Contains(err.Error(), "secret not found")
}

// These methods implement the secretGetterUpdateCreateDeleter interface from community.
// We hardcode here the AppDB sub-path for Vault since community is used only to deploy
// AppDB pods. This allows us to minimize the changes to Community.
// TODO this method is very fishy as it has hardcoded AppDBSecretPath, but is used not only for AppDB
// We should probably use ReadSecret instead -> https://jira.mongodb.org/browse/CLOUDP-277863
func (r SecretClient) GetSecret(ctx context.Context, secretName types.NamespacedName) (corev1.Secret, error) {
	if vault.IsVaultSecretBackend() {
		s := corev1.Secret{}

		data, err := r.ReadSecret(ctx, secretName, r.VaultClient.AppDBSecretPath())
		if err != nil {
			return s, err
		}
		s.Data = make(map[string][]byte)
		for k, v := range data {
			s.Data[k] = []byte(v)
		}
		return s, nil
	}
	return r.KubeClient.GetSecret(ctx, secretName)
}

func (r SecretClient) CreateSecret(ctx context.Context, s corev1.Secret) error {
	var appdbSecretPath string
	if r.VaultClient != nil {
		appdbSecretPath = r.VaultClient.AppDBSecretPath()
	}
	return r.PutSecret(ctx, s, appdbSecretPath)
}

func (r SecretClient) UpdateSecret(ctx context.Context, s corev1.Secret) error {
	if vault.IsVaultSecretBackend() {
		return r.CreateSecret(ctx, s)
	}
	return r.KubeClient.UpdateSecret(ctx, s)
}

func (r SecretClient) DeleteSecret(ctx context.Context, secretName types.NamespacedName) error {
	if vault.IsVaultSecretBackend() {
		// TODO deletion logic
		return nil
	}
	return r.KubeClient.DeleteSecret(ctx, secretName)
}

func DataToStringData(data map[string][]byte) map[string]string {
	stringData := make(map[string]string)
	for k, v := range data {
		stringData[k] = string(v)
	}
	return stringData
}
