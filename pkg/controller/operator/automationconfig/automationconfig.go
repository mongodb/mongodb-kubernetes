package automationconfig

import (
	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnsureSecret fetches the existing Secret and applies the callback to it and pushes changes back.
// The callback is expected to update the data in Secret or return false if no update/create is needed
// Returns the final Secret (could be the initial one or the one after the update)
func EnsureSecret(secretGetUpdateCreator secret.GetUpdateCreator, nsName client.ObjectKey, callback func(*corev1.Secret) bool, owner v1.CustomResourceReadWriter) (corev1.Secret, error) {
	existingSecret, err := secretGetUpdateCreator.GetSecret(nsName)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			newSecret := secret.Builder().
				SetName(nsName.Name).
				SetNamespace(nsName.Namespace).
				SetOwnerReferences(kube.BaseOwnerReference(owner)).
				Build()

			if !callback(&newSecret) {
				return corev1.Secret{}, nil
			}

			if err := secretGetUpdateCreator.CreateSecret(newSecret); err != nil {
				return corev1.Secret{}, err
			}
			return newSecret, nil
		}
		return corev1.Secret{}, err
	}
	// We are updating the existing Secret
	if !callback(&existingSecret) {
		return existingSecret, nil
	}
	if err := secretGetUpdateCreator.UpdateSecret(existingSecret); err != nil {
		return existingSecret, err
	}
	return existingSecret, nil
}
