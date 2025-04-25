package pem

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

// ReadHashFromSecret reads the existing Pem from
// the secret that stores this StatefulSet's Pem collection.
func ReadHashFromSecret(ctx context.Context, secretClient secrets.SecretClient, namespace, name, basePath string, log *zap.SugaredLogger) string {
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
		s, err := secretClient.KubeClient.GetSecret(ctx, kube.ObjectKey(namespace, name))
		if err != nil {
			log.Debugf("tls secret %s doesn't exist yet, unable to compute hash of pem", name)
			return ""
		}

		if s.Type != corev1.SecretTypeTLS {
			log.Debugf("tls secret %s is not of type corev1.SecretTypeTLS; we will not use hash as key name", name)
			return ""
		}

		secretData = secrets.DataToStringData(s.Data)
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
