package service

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
)

func DeleteServiceIfItExists(ctx context.Context, getterDeleter service.GetDeleter, serviceName types.NamespacedName) error {
	_, err := getterDeleter.GetService(ctx, serviceName)
	if err != nil {
		// If it is not found return
		if apiErrors.IsNotFound(err) {
			return nil
		}
		// Otherwise we got an error when trying to get it
		return fmt.Errorf("can't get service %s: %s", serviceName, err)
	}
	return getterDeleter.DeleteService(ctx, serviceName)
}

// Merge merges `source` into `dest`. Both arguments will remain unchanged
// a new service will be created and returned.
// The "merging" process is arbitrary and it only handle specific attributes
func Merge(dest corev1.Service, source corev1.Service) corev1.Service {
	if dest.Annotations == nil {
		dest.Annotations = map[string]string{}
	}
	for k, v := range source.Annotations {
		dest.Annotations[k] = v
	}

	if dest.Labels == nil {
		dest.Labels = map[string]string{}
	}

	for k, v := range source.Labels {
		dest.Labels[k] = v
	}

	if dest.Spec.Selector == nil {
		dest.Spec.Selector = map[string]string{}
	}

	for k, v := range source.Spec.Selector {
		dest.Spec.Selector[k] = v
	}

	cachedNodePorts := map[int32]int32{}
	for _, port := range dest.Spec.Ports {
		cachedNodePorts[port.Port] = port.NodePort
	}

	if len(source.Spec.Ports) > 0 {
		portCopy := make([]corev1.ServicePort, len(source.Spec.Ports))
		copy(portCopy, source.Spec.Ports)
		dest.Spec.Ports = portCopy

		for i := range dest.Spec.Ports {
			// Source might not specify NodePort and we shouldn't override existing NodePort value
			if dest.Spec.Ports[i].NodePort == 0 {
				dest.Spec.Ports[i].NodePort = cachedNodePorts[dest.Spec.Ports[i].Port]
			}
		}
	}

	dest.Spec.Type = source.Spec.Type
	dest.Spec.LoadBalancerIP = source.Spec.LoadBalancerIP
	dest.Spec.ExternalTrafficPolicy = source.Spec.ExternalTrafficPolicy
	return dest
}

// CreateOrUpdateService will create or update a service in Kubernetes.
func CreateOrUpdateService(ctx context.Context, getUpdateCreator service.GetUpdateCreator, desiredService corev1.Service) error {
	namespacedName := types.NamespacedName{Namespace: desiredService.Namespace, Name: desiredService.Name}
	existingService, err := getUpdateCreator.GetService(ctx, namespacedName)

	if err != nil {
		if apiErrors.IsNotFound(err) {
			err = getUpdateCreator.CreateService(ctx, desiredService)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		mergedService := Merge(existingService, desiredService)
		err = getUpdateCreator.UpdateService(ctx, mergedService)
		if err != nil {
			return err
		}
	}
	return nil
}
