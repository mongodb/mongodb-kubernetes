package contains

import (
	"reflect"
	"slices"

	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
)

func String(slice []string, s string) bool {
	return slices.Contains(slice, s)
}

func Sha256(slice []string) bool {
	return String(slice, constants.Sha256)
}

func Sha1(slice []string) bool {
	return String(slice, constants.Sha1)
}

func X509(slice []string) bool {
	return String(slice, constants.X509)
}

func NamespacedName(nsNames []types.NamespacedName, nsName types.NamespacedName) bool {
	return slices.Contains(nsNames, nsName)
}

func AccessMode(accessModes []corev1.PersistentVolumeAccessMode, mode corev1.PersistentVolumeAccessMode) bool {
	return slices.Contains(accessModes, mode)
}

func OwnerReferences(ownerRefs []metav1.OwnerReference, ownerRef metav1.OwnerReference) bool {
	for _, elem := range ownerRefs {
		if reflect.DeepEqual(elem, ownerRef) {
			return true
		}
	}
	return false
}
