package service

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
)

// CreateOrUpdateService will create or update a service in Kubernetes.
func CreateOrUpdateService(getUpdateCreator service.GetUpdateCreator, desiredService corev1.Service, log *zap.SugaredLogger) error {
	log = log.With("service", desiredService.ObjectMeta.Name)
	namespacedName := kube.ObjectKey(desiredService.ObjectMeta.Namespace, desiredService.ObjectMeta.Name)

	existingService, err := getUpdateCreator.GetService(namespacedName)
	method := ""
	if err != nil {
		if apiErrors.IsNotFound(err) {
			err = getUpdateCreator.CreateService(desiredService)
			if err != nil {
				return err
			}
		} else {
			return err
		}
		method = "Created"
	} else {
		mergedService := service.Merge(existingService, desiredService)
		err = getUpdateCreator.UpdateService(mergedService)
		if err != nil {
			return err
		}
		method = "Updated"
	}

	log.Debugw(fmt.Sprintf("%s Service", method), "type", desiredService.Spec.Type, "port", desiredService.Spec.Ports[0])
	return nil
}
