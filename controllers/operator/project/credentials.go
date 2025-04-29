package project

import (
	"context"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

// ReadCredentials reads the Secret containing the credentials to authenticate in Ops Manager and creates a matching 'Credentials' object
func ReadCredentials(ctx context.Context, secretClient secrets.SecretClient, credentialsSecret client.ObjectKey, log *zap.SugaredLogger) (mdbv1.Credentials, error) {
	var operatorSecretPath string
	if vault.IsVaultSecretBackend() {
		operatorSecretPath = secretClient.VaultClient.OperatorSecretPath()
	}
	secret, err := secretClient.ReadSecret(ctx, credentialsSecret, operatorSecretPath)
	if err != nil {
		return mdbv1.Credentials{}, err
	}
	oldSecretEntries, user, publicAPIKey := secretContainsPairOfKeys(secret, util.OldOmUser, util.OldOmPublicApiKey)

	newSecretEntries, publicKey, privateKey := secretContainsPairOfKeys(secret, util.OmPublicApiKey, util.OmPrivateKey)

	if !oldSecretEntries && !newSecretEntries {
		return mdbv1.Credentials{}, xerrors.Errorf("secret %s does not contain the required entries. It should contain either %s and %s, or %s and %s", credentialsSecret, util.OldOmUser, util.OldOmPublicApiKey, util.OmPublicApiKey, util.OmPrivateKey)
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
