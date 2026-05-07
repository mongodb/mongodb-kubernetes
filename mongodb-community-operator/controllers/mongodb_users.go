package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/constants"
)

const (
	// connectionStringSecretOwnerNamespaceAnnotation stores the namespace of the
	// MongoDBCommunity resource that owns the generated connection string secret.
	connectionStringSecretOwnerNamespaceAnnotation = "mongodbcommunity.mongodb.com/connection-string-owner-namespace"

	// connectionStringSecretOwnerNameAnnotation stores the name of the
	// MongoDBCommunity resource that owns the generated connection string secret.
	connectionStringSecretOwnerNameAnnotation = "mongodbcommunity.mongodb.com/connection-string-owner-name"

	// connectionStringSecretOwnerUIDAnnotation stores the UID of the
	// MongoDBCommunity resource that owns the generated connection string secret.
	// The UID is included so the operator can distinguish a recreated resource
	// from an older resource with the same namespace/name.
	connectionStringSecretOwnerUIDAnnotation = "mongodbcommunity.mongodb.com/connection-string-owner-uid"
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

// connectionStringSecretOwnerReferences returns the owner references that should
// be set on a generated connection string secret.
//
// Same-namespace connection string secrets can safely use the normal
// MongoDBCommunity owner reference and continue to participate in standard
// Kubernetes garbage collection.
//
// Cross-namespace connection string secrets must not use owner references,
// because Kubernetes does not allow a namespaced owner to own a dependent in a
// different namespace. In that case this function returns nil.
func connectionStringSecretOwnerReferences(mdb mdbv1.MongoDBCommunity, secretNamespace string) []metav1.OwnerReference {
	if secretNamespace != mdb.Namespace {
		return nil
	}
	return mdb.GetOwnerReferences()
}

// connectionStringSecretAnnotations builds the annotations for a generated
// connection string secret.
//
// Any user-provided connection string secret annotations are preserved.
// In addition, the owning MongoDBCommunity resource identity is stored in
// annotations so that cross-namespace connection string secrets can still be
// recognized as operator-managed even though they cannot use owner references.
func connectionStringSecretAnnotations(
	mdb mdbv1.MongoDBCommunity,
	userAnnotations map[string]string,
) map[string]string {
	annotations := make(map[string]string, len(userAnnotations)+3)
	for k, v := range userAnnotations {
		annotations[k] = v
	}
	annotations[connectionStringSecretOwnerNamespaceAnnotation] = mdb.Namespace
	annotations[connectionStringSecretOwnerNameAnnotation] = mdb.Name
	annotations[connectionStringSecretOwnerUIDAnnotation] = string(mdb.UID)
	return annotations
}

func isManagedConnectionStringSecret(existingSecret corev1.Secret, mdb mdbv1.MongoDBCommunity) bool {
	if secret.HasOwnerReferences(existingSecret, mdb.GetOwnerReferences()) {
		return true
	}

	annotations := existingSecret.GetAnnotations()
	return annotations[connectionStringSecretOwnerNamespaceAnnotation] == mdb.Namespace &&
		annotations[connectionStringSecretOwnerNameAnnotation] == mdb.Name &&
		annotations[connectionStringSecretOwnerUIDAnnotation] == string(mdb.UID)
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
		if err == nil && !isManagedConnectionStringSecret(existingSecret, mdb) {
			return fmt.Errorf("connection string secret %s/%s already exists and is not managed by the operator", secretNamespace, secretName)
		}

		pwd := ""

		if user.Database != constants.ExternalDB {
			secretNamespacedName := types.NamespacedName{Name: user.PasswordSecretName, Namespace: mdb.Namespace}
			pwd, err = secret.ReadKey(ctx, r.client, user.PasswordSecretKey, secretNamespacedName)
			if err != nil {
				return err
			}
		}

		connectionStringSecret := secret.Builder().
			SetName(secretName).
			SetNamespace(secretNamespace).
			SetAnnotations(connectionStringSecretAnnotations(mdb, user.ConnectionStringSecretAnnotations)).
			SetField("connectionString.standard", mdb.MongoAuthUserURI(user, pwd)).
			SetField("connectionString.standardSrv", mdb.MongoAuthUserSRVURI(user, pwd)).
			SetField("username", user.Username).
			SetField("password", pwd).
			SetOwnerReferences(connectionStringSecretOwnerReferences(mdb, secretNamespace)).
			Build()

		if err := secret.CreateOrUpdate(ctx, r.client, connectionStringSecret); err != nil {
			return err
		}

		secretNamespacedName := types.NamespacedName{Name: connectionStringSecret.Name, Namespace: connectionStringSecret.Namespace}
		r.secretWatcher.Watch(ctx, secretNamespacedName, mdb.NamespacedName())
	}

	return nil
}
