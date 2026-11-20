// Package operatoridentity provides the operator with the logical name of the member
// cluster it runs on read from a ConfigMap.
//
// The identity is read once at startup; the Watcher restarts the operator when the
// ConfigMap's effective value changes.
package operatoridentity

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
)

// Load reads the operator's cluster identity directly from the API (the
// identity watcher's cache is not started yet at startup).
//
// A missing ConfigMap means no identity (e.g. basic installation without extra
// configuration). A ConfigMap with a missing or empty clusterName key is
// malformed and returns an error.
func Load(ctx context.Context, c client.Reader, namespace string) (string, error) {
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, types.NamespacedName{Name: resourcenames.MemberClusterIdentityConfigMapName, Namespace: namespace}, cm)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	identity := cm.Data[resourcenames.MemberClusterIdentityClusterNameKey]
	if identity == "" {
		return "", fmt.Errorf("identity ConfigMap %s/%s has no %q data key or it is empty", namespace, resourcenames.MemberClusterIdentityConfigMapName, resourcenames.MemberClusterIdentityClusterNameKey)
	}
	return identity, nil
}
