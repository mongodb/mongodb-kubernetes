package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/connectionstringsecret"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
)

// ensureUserResources will check that the configured user password secrets can be found
// and will start monitor them so that the reconcile process is triggered every time these secrets are updated
func (r ReplicaSetReconciler) ensureUserResources(ctx context.Context, mdb mdbv1.MongoDBCommunity) error {
	for _, user := range mdb.GetAuthUsers() {
		if user.Database != constants.ExternalDB {
			secretNamespacedName := types.NamespacedName{Name: user.PasswordSecretName, Namespace: mdb.Namespace}
			if _, err := secret.ReadKey(ctx, r.client, user.PasswordSecretKey, secretNamespacedName); err != nil {
				if apiErrors.IsNotFound(err) {
					// check for SCRAM secret as well
					scramSecretName := types.NamespacedName{Name: user.ScramCredentialsSecretName, Namespace: mdb.Namespace}
					_, err = r.client.GetSecret(ctx, scramSecretName)
					if apiErrors.IsNotFound(err) {
						return fmt.Errorf(`user password secret: %s and scram secret: %s not found`, secretNamespacedName, scramSecretName)
					}
					r.log.Errorf(`user password secret "%s" not found: %s`, secretNamespacedName, err)
					continue
				}
				return err
			}
			r.secretWatcher.Watch(ctx, secretNamespacedName, mdb.NamespacedName())
		}
	}

	return nil
}

// updateConnectionStringSecrets updates secrets where user specific connection strings are stored.
// The client applications can mount these secrets and connect to the mongodb cluster
func (r ReplicaSetReconciler) updateConnectionStringSecrets(ctx context.Context, mdb mdbv1.MongoDBCommunity) error {
	for _, user := range mdb.GetAuthUsers() {
		secretName := user.ConnectionStringSecretName

		secretNamespace := mdb.Namespace
		if user.ConnectionStringSecretNamespace != "" {
			secretNamespace = user.ConnectionStringSecretNamespace
		}

		existingSecret, err := r.client.GetSecret(ctx, types.NamespacedName{
			Name:      secretName,
			Namespace: secretNamespace,
		})
		if err != nil && !apiErrors.IsNotFound(err) {
			return err
		}
		if err == nil {
			if err := connectionstringsecret.ValidateOwnership(existingSecret, &mdb); err != nil {
				return err
			}
		}

		pwd := ""

		if user.Database != constants.ExternalDB {
			secretNamespacedName := types.NamespacedName{Name: user.PasswordSecretName, Namespace: mdb.Namespace}
			pwd, err = secret.ReadKey(ctx, r.client, user.PasswordSecretKey, secretNamespacedName)
			if err != nil {
				return err
			}
		}

		// External users have no password, so the password field is left out
		// rather than written empty.
		err = connectionstringsecret.PublishForUser(ctx, r.client, mdb.ConnectionOptions(), user, pwd, connectionstringsecret.Secret{
			Name:            secretName,
			Namespace:       secretNamespace,
			Annotations:     user.ConnectionStringSecretAnnotations,
			OwnerReferences: mdb.GetOwnerReferences(),
		})
		if err != nil {
			return err
		}

		secretNamespacedName := types.NamespacedName{Name: secretName, Namespace: secretNamespace}
		r.secretWatcher.Watch(ctx, secretNamespacedName, mdb.NamespacedName())
	}

	return nil
}
