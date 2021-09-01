package project

import (
	"fmt"
	"strings"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"go.uber.org/zap"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadCredentials reads the Secret containing the credentials to authenticate in Ops Manager and creates a matching 'Credentials' object
func ReadCredentials(secretGetter secret.Getter, credentialsSecret client.ObjectKey, log *zap.SugaredLogger) (mdbv1.Credentials, error) {
	secret, err := readSecret(secretGetter, credentialsSecret)
	if err != nil {
		return mdbv1.Credentials{}, err
	}

	oldSecretEntries, user, publicAPIKey := secretContainsPairOfKeys(secret, util.OldOmUser, util.OldOmPublicApiKey)

	newSecretEntries, publicKey, privateKey := secretContainsPairOfKeys(secret, util.OmPublicApiKey, util.OmPrivateKey)

	if !(oldSecretEntries || newSecretEntries) {
		return mdbv1.Credentials{}, fmt.Errorf("secret %s does not contain the required entries. It should contain either %s and %s, or %s and %s", credentialsSecret, util.OldOmUser, util.OldOmPublicApiKey, util.OmPublicApiKey, util.OmPrivateKey)
	}

	if oldSecretEntries {
		log.Infof("Usage of old entries for the credentials secret (\"%s\" and \"%s\") is deprecated, prefer using \"%s\" and \"%s\"", util.OldOmUser, util.OldOmPublicApiKey, util.OmPublicApiKey, util.OmPrivateKey)
		return mdbv1.Credentials{
			PublicAPIKey:  user,
			PrivateAPIKey: publicAPIKey,
		}, nil
	}

	return mdbv1.Credentials{
		PublicAPIKey:  publicKey,
		PrivateAPIKey: privateKey,
	}, nil

}

func secretContainsPairOfKeys(secret map[string]string, key1 string, key2 string) (bool, string, string) {
	val1, ok := secret[key1]
	if !ok {
		return false, "", ""
	}
	val2, ok := secret[key2]
	if !ok {
		return false, "", ""
	}
	return true, val1, val2
}

// TODO use a SecretsClient the same we do for ConfigMapClient
func readSecret(secretGetter secret.Getter, nsName client.ObjectKey) (map[string]string, error) {
	secrets := make(map[string]string)
	stringData, err := secret.ReadStringData(secretGetter, nsName)
	if err != nil {
		return nil, err
	}
	for k, v := range stringData {
		secrets[k] = strings.TrimSuffix(string(v[:]), "\n")
	}
	return secrets, nil
}
