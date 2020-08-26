package project

import (
	"fmt"
	"strings"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadCredentials reads the Secret containing the credentials to authenticate in Ops Manager and creates a matching 'Credentials' object
func ReadCredentials(secretGetter secret.Getter, credentialsSecret client.ObjectKey) (mdbv1.Credentials, error) {
	secret, err := readSecret(secretGetter, credentialsSecret)
	if err != nil {
		return mdbv1.Credentials{}, fmt.Errorf("error getting secret %s: %s", credentialsSecret, err)
	}
	publicAPIKey, ok := secret[util.OmPublicApiKey]
	if !ok {
		return mdbv1.Credentials{}, fmt.Errorf(`property "%s" is not specified in Secret %s`, util.OmPublicApiKey, credentialsSecret)
	}
	user, ok := secret[util.OmUser]
	if !ok {
		return mdbv1.Credentials{}, fmt.Errorf(`property "%s" is not specified in Secret %s`, util.OmUser, credentialsSecret)
	}

	return mdbv1.Credentials{
		User:         user,
		PublicAPIKey: publicAPIKey,
	}, nil
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
