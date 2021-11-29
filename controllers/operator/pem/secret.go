package pem

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"go.uber.org/zap"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
)

// CreateOrUpdateSecret will create (if it does not exist) or update (if it does) a secret.
func CreateOrUpdateSecret(secretGetUpdateCreator secret.GetUpdateCreator, name, namespace string, pemCollection *Collection) error {
	secretToCreate, err := secretGetUpdateCreator.GetSecret(kube.ObjectKey(namespace, name))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			pemFilesSecret := secret.Builder().
				SetName(name).
				SetNamespace(namespace).
				SetStringData(pemCollection.Merge()).
				Build()
			// assume the secret was not found, need to create it
			// leave a nil owner reference as we haven't decided yet if we need to remove certificates
			return secretGetUpdateCreator.CreateSecret(pemFilesSecret)
		}
		return err
	}

	// if the secret already exists, it might contain entries that we want merged:
	// for each Pod we'll have the key and the certificate, but we might also have the
	// certificate added in several stages. If a certificate/key exists, and this

	pemData := pemCollection.MergeWith(secretToCreate.Data)
	secretToCreate.StringData = pemData
	return secretGetUpdateCreator.UpdateSecret(secretToCreate)
}

// ReadHashFromSecret reads the existing Pem from
// the secret that stores this StatefulSet's Pem collection.
func ReadHashFromSecret(secretClient secrets.SecretClient, namespace, name string, basePath string, log *zap.SugaredLogger) string {
	var secretData map[string]string
	var err error
	if vault.IsVaultSecretBackend() {
		path := fmt.Sprintf("%s/%s/%s", basePath, namespace, name)
		secretData, err = secretClient.VaultClient.ReadSecretString(path)
		if err != nil {
			log.Debugf("tls secret %s doesn't exist yet, unable to compute hash of pem", name)
			return ""
		}
	} else {
		secretData, err = secret.ReadStringData(secretClient.KubeClient, kube.ObjectKey(namespace, name))
		if err != nil {
			log.Debugf("tls secret %s doesn't exist yet, unable to compute hash of pem", name)
			return ""
		}
	}
	return ReadHashFromData(secretData, log)
}

func ReadHashFromData(secretData map[string]string, log *zap.SugaredLogger) string {
	pemCollection := NewCollection()
	for k, v := range secretData {
		pemCollection.MergeEntry(k, NewFileFrom(v))
	}
	pemHash, err := pemCollection.GetHash()
	if err != nil {
		log.Errorf("error computing pem hash: %s", err)
		return ""
	}
	return pemHash
}
