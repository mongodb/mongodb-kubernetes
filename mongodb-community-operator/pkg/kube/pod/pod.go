package pod

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
)

type Getter interface {
	GetPod(ctx context.Context, objectKey client.ObjectKey) (corev1.Pod, error)
}
