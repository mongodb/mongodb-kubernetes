package searchcontroller

import (
	"context"
	"maps"
	"slices"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// MountedContent identifies Secret/ConfigMap content that is mounted into a
// workload but not embedded in its rendered config, so a content change must
// still roll the workload (for example certificate, CA, or key-password
// rotation).
type MountedContent struct {
	Secrets    []types.NamespacedName
	ConfigMaps []types.NamespacedName
}

// HashMountedContent returns a stable hash over the full content of the given
// Secrets and ConfigMaps, or "" when none are set. Empty names are skipped; a
// missing object is an error. Callers fold the result into a config or
// pod-template hash so that mounted material rotation rolls the workload.
func HashMountedContent(ctx context.Context, c kubernetesClient.Client, content MountedContent) (string, error) {
	var combined []byte
	for _, nn := range content.Secrets {
		if nn.Name == "" {
			continue
		}
		s, err := c.GetSecret(ctx, nn)
		if err != nil {
			return "", xerrors.Errorf("reading Secret %s/%s: %w", nn.Namespace, nn.Name, err)
		}
		for _, key := range slices.Sorted(maps.Keys(s.Data)) {
			combined = append(combined, []byte("s/"+nn.Name+"/"+key+":"+string(s.Data[key])+";")...)
		}
	}
	for _, nn := range content.ConfigMaps {
		if nn.Name == "" {
			continue
		}
		cm := &corev1.ConfigMap{}
		if err := c.Get(ctx, nn, cm); err != nil {
			return "", xerrors.Errorf("reading ConfigMap %s/%s: %w", nn.Namespace, nn.Name, err)
		}
		for _, key := range slices.Sorted(maps.Keys(cm.Data)) {
			combined = append(combined, []byte("c/"+nn.Name+"/"+key+":"+cm.Data[key]+";")...)
		}
	}
	if len(combined) == 0 {
		return "", nil
	}
	return hashBytes(combined), nil
}
