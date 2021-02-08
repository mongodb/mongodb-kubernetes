package create

import (
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"
	enterprisesvc "github.com/10gen/ops-manager-kubernetes/pkg/kube/service"
	enterprisests "github.com/10gen/ops-manager-kubernetes/pkg/kube/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

var (
	externalConnectivityPortName = "external-connectivity-port"
	backupPortName               = "backup-port"
	appLabelKey                  = "app"
)

// DatabaseInKubernetes creates (updates if it exists) the StatefulSet with its Service.
// It returns any errors coming from Kubernetes API.
func DatabaseInKubernetes(client kubernetesClient.Client, mdb mdbv1.MongoDB, sts appsv1.StatefulSet, config func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) error {
	opts := config(mdb)
	set, err := enterprisests.CreateOrUpdateStatefulset(client,
		mdb.Namespace,
		log,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := kube.ObjectKey(mdb.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &mdb, set.Spec.ServiceName, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(client, internalService, log)
	if err != nil {
		return err
	}

	if mdb.Spec.ExposedExternally {
		namespacedName := kube.ObjectKey(mdb.Namespace, set.Spec.ServiceName+"-external")
		externalService := buildService(namespacedName, &mdb, set.Spec.ServiceName, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeNodePort})
		return enterprisesvc.CreateOrUpdateService(client, externalService, log)
	}

	return nil
}

// AppDBInKubernetes creates or updates the StatefulSet and Service required for the AppDB.
func AppDBInKubernetes(client kubernetesClient.Client, opsManager omv1.MongoDBOpsManager, sts appsv1.StatefulSet, config construct.AppDBConfiguration, log *zap.SugaredLogger) error {
	opts := config(opsManager)
	set, err := enterprisests.CreateOrUpdateStatefulset(client,
		opsManager.Namespace,
		log,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &opsManager, set.Spec.ServiceName, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	return enterprisesvc.CreateOrUpdateService(client, internalService, log)
}

// BackupDaemonInKubernetes creates or updates the StatefulSet and Services required.
func BackupDaemonInKubernetes(client kubernetesClient.Client, opsManager omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) (bool, error) {
	set, err := enterprisests.CreateOrUpdateStatefulset(
		client,
		opsManager.Namespace,
		log,
		&sts,
	)

	if err != nil {
		// Check if it is a k8s error or a custom one
		if _, ok := err.(enterprisests.StatefulSetCantBeUpdatedError); !ok {
			return false, err
		}
		// In this case, we delete the old Statefulset
		log.Debug("Deleting the old backup stateful set and creating a new one")
		stsNamespacedName := kube.ObjectKey(opsManager.Namespace, opsManager.BackupStatefulSetName())
		err = client.DeleteStatefulSet(stsNamespacedName)
		if err != nil {
			return false, fmt.Errorf("failed while trying to delete previous backup daemon statefulset: %s", err)
		}
		return true, nil
	}
	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &opsManager, set.Spec.ServiceName, construct.BackupDaemonServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(client, internalService, log)
	return false, err
}

// OpsManagerInKubernetes creates all of the required Kubernetes resources for Ops Manager.
// It creates the StatefulSet and all required services.
func OpsManagerInKubernetes(client kubernetesClient.Client, opsManager omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) error {
	set, err := enterprisests.CreateOrUpdateStatefulset(client,
		opsManager.Namespace,
		log,
		&sts,
	)
	if err != nil {
		return err
	}

	_, port := opsManager.GetSchemePort()

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &opsManager, set.Spec.ServiceName, int32(port), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(client, internalService, log)
	if err != nil {
		return err
	}

	var externalService *corev1.Service = nil
	if opsManager.Spec.MongoDBOpsManagerExternalConnectivity != nil {
		namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName+"-ext")
		svc := buildService(namespacedName, &opsManager, set.Spec.ServiceName, int32(port), *opsManager.Spec.MongoDBOpsManagerExternalConnectivity)
		externalService = &svc
	}

	// Need to create queryable backup service
	var backupService *corev1.Service = nil
	if opsManager.Spec.Backup.Enabled {
		if opsManager.Spec.MongoDBOpsManagerExternalConnectivity != nil {
			if err := addQueryableBackupPortToExternalService(opsManager, externalService); err != nil {
				return err
			}
		} else if backupService, err = buildBackupService(opsManager, set.Spec.ServiceName+"-backup"); err != nil {
			return err
		}
	}

	if externalService != nil {
		if err := enterprisesvc.CreateOrUpdateService(client, *externalService, log); err != nil {
			return err
		}
	}

	if backupService != nil {
		if err := enterprisesvc.CreateOrUpdateService(client, *backupService, log); err != nil {
			return err
		}
	}

	return nil
}

// addQueryableBackupPortToExternalService adds the backup port to the existing external Ops Manager service.
// this function assumes externalService is not nil.
func addQueryableBackupPortToExternalService(opsManager omv1.MongoDBOpsManager, externalService *corev1.Service) error {
	backupSvcPort, err := opsManager.Spec.BackupSvcPort()
	if err != nil {
		return fmt.Errorf("can't parse queryable backup port: %s", err)
	}
	externalService.Spec.Ports[0].Name = externalConnectivityPortName
	externalService.Spec.Ports = append(externalService.Spec.Ports, corev1.ServicePort{
		Name: backupPortName,
		Port: backupSvcPort,
	})
	return nil
}

// buildBackupService returns the service needed for queryable backup.
func buildBackupService(opsManager omv1.MongoDBOpsManager, serviceName string) (*corev1.Service, error) {
	backupSvcPort, err := opsManager.Spec.BackupSvcPort()
	if err != nil {
		return nil, fmt.Errorf("can't parse queryable backup port: %s", err)
	}

	// Otherwise create a new service
	namespacedName := kube.ObjectKey(opsManager.Namespace, serviceName)
	svc := buildService(namespacedName, &opsManager, "ops-manager-backup", backupSvcPort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeLoadBalancer})
	return &svc, nil
}

// buildService creates the Kube Service. If it should be seen externally it makes it of type NodePort that will assign
// some random port in the range 30000-32767
// Note that itself service has no dedicated IP by default ("clusterIP: None") as all mongo entities should be directly
// addressable.
// This function will update a Service object if passed, or return a new one if passed nil, this is to be able to update
// Services and to not change any attribute they might already have that needs to be maintained.
func buildService(namespacedName types.NamespacedName, owner v1.CustomResourceReadWriter, label string, port int32, mongoServiceDefinition omv1.MongoDBOpsManagerServiceDefinition) corev1.Service {
	labels := map[string]string{
		appLabelKey:                   label,
		construct.ControllerLabelName: util.OperatorName,
	}
	svcBuilder := service.Builder().
		SetNamespace(namespacedName.Namespace).
		SetName(namespacedName.Name).
		SetPort(port).
		SetOwnerReferences(kube.BaseOwnerReference(owner)).
		SetLabels(labels).
		SetSelector(labels).
		SetServiceType(mongoServiceDefinition.Type)

	serviceType := mongoServiceDefinition.Type
	if serviceType == corev1.ServiceTypeNodePort || serviceType == corev1.ServiceTypeLoadBalancer {
		svcBuilder.SetClusterIP("").SetNodePort(mongoServiceDefinition.Port)
	}

	if serviceType == corev1.ServiceTypeClusterIP {
		svcBuilder.SetPublishNotReadyAddresses(true).SetClusterIP("None").SetPortName("mongodb")
	}

	if mongoServiceDefinition.Annotations != nil {
		svcBuilder.SetAnnotations(mongoServiceDefinition.Annotations)
	}

	if mongoServiceDefinition.LoadBalancerIP != "" {
		svcBuilder.SetLoadBalancerIP(mongoServiceDefinition.LoadBalancerIP)
	}

	if mongoServiceDefinition.ExternalTrafficPolicy != "" {
		svcBuilder.SetExternalTrafficPolicy(mongoServiceDefinition.ExternalTrafficPolicy)
	}

	return svcBuilder.Build()
}
