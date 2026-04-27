package deployment

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sClient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
)

// GetDeploymentStatus returns the workflow.Status based on the readiness of the Deployment.
// If the Deployment is not ready the request will be retried in 3 seconds (instead of the default 10 seconds)
// allowing to reach "ready" status sooner.
//
// `expectedGeneration` is the `meta.generation` returned from create/update deployment API calls.
// It is used to compare with `status.observedGeneration` to avoid reading stale Deployment objects.
func GetDeploymentStatus(ctx context.Context, namespace, name string, expectedGeneration int64, c k8sClient.Client) workflow.Status {
	dep := &appsv1.Deployment{}
	key := k8sClient.ObjectKey{Namespace: namespace, Name: name}
	err := c.Get(ctx, key, dep)
	i := 0

	// Sometimes it is possible that the Deployment which has just been created
	// returns a not found error when getting it too soon afterward.
	for apierrors.IsNotFound(err) && i < 10 {
		i++
		zap.S().Debugf("Deployment was not found: %s, attempt %d", err, i)
		time.Sleep(time.Second * 1)
		err = c.Get(ctx, key, dep)
	}

	if err != nil {
		return workflow.Failed(err)
	}

	wantedReplicas := int32(1)
	if dep.Spec.Replicas != nil {
		wantedReplicas = *dep.Spec.Replicas
	}

	ready := dep.Status.ReadyReplicas == wantedReplicas &&
		dep.Status.UpdatedReplicas == wantedReplicas &&
		dep.Status.ObservedGeneration == dep.Generation &&
		dep.Status.ObservedGeneration == expectedGeneration

	if !ready {
		zap.S().Debugf("Deployment %s/%s (wanted: %d, ready: %d, updated: %d, generation: %d, observedGeneration: %d, expectedGeneration: %d)",
			namespace, name, wantedReplicas, dep.Status.ReadyReplicas, dep.Status.UpdatedReplicas,
			dep.Generation, dep.Status.ObservedGeneration, expectedGeneration)
		msg := fmt.Sprintf("Not all the Pods are ready (wanted: %d, updated: %d, ready: %d)", wantedReplicas, dep.Status.UpdatedReplicas, dep.Status.ReadyReplicas)
		return workflow.Pending("%s", msg).
			WithResourcesNotReady([]status.ResourceNotReady{{
				Kind:    status.DeploymentKind,
				Name:    name,
				Message: msg,
			}}).
			WithRetry(3)
	}

	zap.S().Debugf("Deployment %s/%s is ready on check attempt #%d", namespace, name, i)
	return workflow.OK()
}
