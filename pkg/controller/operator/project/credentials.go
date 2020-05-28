package project

import (
	"context"
	"fmt"
	"strings"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadCredentials reads the Secret containing the credentials to authenticate in Ops Manager and creates a matching 'Credentials' object
func ReadCredentials(client client.Client, credentialsSecret client.ObjectKey) (mdbv1.Credentials, error) {
	secret, err := readSecret(client, credentialsSecret)
	if err != nil {
		return mdbv1.Credentials{}, fmt.Errorf("Error getting secret %s: %s", credentialsSecret, err)
	}
	publicAPIKey, ok := secret[util.OmPublicApiKey]
	if !ok {
		return mdbv1.Credentials{}, fmt.Errorf("Property \"%s\" is not specified in Secret %s", util.OmPublicApiKey, credentialsSecret)
	}
	user, ok := secret[util.OmUser]
	if !ok {
		return mdbv1.Credentials{}, fmt.Errorf("Property \"%s\" is not specified in Secret %s", util.OmUser, credentialsSecret)
	}

	return mdbv1.Credentials{
		User:         user,
		PublicAPIKey: publicAPIKey,
	}, nil
}

// TODO use a SecretsClient the same we do for ConfigMapClient
func readSecret(client client.Client, nsName client.ObjectKey) (map[string]string, error) {
	secret := &corev1.Secret{}
	e := client.Get(context.TODO(), nsName, secret)
	if e != nil {
		return nil, e
	}

	secrets := make(map[string]string)
	for k, v := range secret.Data {
		secrets[k] = strings.TrimSuffix(string(v[:]), "\n")
	}
	return secrets, nil
}
