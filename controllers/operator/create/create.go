package create

import (
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	enterprisests "github.com/10gen/ops-manager-kubernetes/pkg/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var (
	externalConnectivityPortName = "external-connectivity-port"
	internalConnectivityPortName = "internal-connectivity-port"
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

	// Adds Prometheus Port if Prometheus has been enabled.
	prom := mdb.GetPrometheus()
	if prom != nil {
		internalService.Spec.Ports = append(internalService.Spec.Ports, corev1.ServicePort{Port: int32(prom.GetPort()), Name: "prometheus"})
	}
	err = service.CreateOrUpdateService(client, internalService)
	if err != nil {
		return err
	}

	namespacedName = kube.ObjectKey(mdb.Namespace, set.Spec.ServiceName+"-external")
	if !mdb.Spec.ExposedExternally {
		return service.DeleteServiceIfItExists(client, namespacedName)
	}

	externalService := buildService(namespacedName, &mdb, set.Spec.ServiceName, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeNodePort})
	return service.CreateOrUpdateService(client, externalService)

}

// AppDBInKubernetes creates or updates the StatefulSet and Service required for the AppDB.
func AppDBInKubernetes(client kubernetesClient.Client, opsManager omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) error {

	set, err := enterprisests.CreateOrUpdateStatefulset(client,
		opsManager.Namespace,
		log,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &opsManager, set.Spec.ServiceName, opsManager.Spec.AppDB.AdditionalMongodConfig.GetPortOrDefault(), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})

	// Adds Prometheus Port if Prometheus has been enabled.
	prom := opsManager.Spec.AppDB.Prometheus
	if prom != nil {
		internalService.Spec.Ports = append(internalService.Spec.Ports, corev1.ServicePort{Port: int32(prom.GetPort()), Name: "prometheus"})
	}

	return service.CreateOrUpdateService(client, internalService)
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
	err = service.CreateOrUpdateService(client, internalService)
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
	// add queryable backup port to service
	if opsManager.Spec.Backup.Enabled {
		if err := addQueryableBackupPortToService(opsManager, &internalService, internalConnectivityPortName); err != nil {
			return err
		}
	}

	err = service.CreateOrUpdateService(client, internalService)
	if err != nil {
		return err
	}

	namespacedName = kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName+"-ext")
	var externalService *corev1.Service = nil
	if opsManager.Spec.MongoDBOpsManagerExternalConnectivity != nil {
		svc := buildService(namespacedName, &opsManager, set.Spec.ServiceName, int32(port), *opsManager.Spec.MongoDBOpsManagerExternalConnectivity)
		externalService = &svc
	} else {
		if err := service.DeleteServiceIfItExists(client, namespacedName); err != nil {
			return err
		}

	}

	// Need to create queryable backup service
	if opsManager.Spec.Backup.Enabled {
		if opsManager.Spec.MongoDBOpsManagerExternalConnectivity != nil {
			if err := addQueryableBackupPortToService(opsManager, externalService, externalConnectivityPortName); err != nil {
				return err
			}
		}
	}

	if externalService != nil {
		if err := service.CreateOrUpdateService(client, *externalService); err != nil {
			return err
		}
	}
	return nil
}

// addQueryableBackupPortToService adds the backup port to the existing external Ops Manager service.
// this function assumes externalService is not nil.
func addQueryableBackupPortToService(opsManager omv1.MongoDBOpsManager, service *corev1.Service, portName string) error {
	backupSvcPort, err := opsManager.Spec.BackupSvcPort()
	if err != nil {
		return fmt.Errorf("can't parse queryable backup port: %s", err)
	}
	service.Spec.Ports[0].Name = portName
	service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
		Name: backupPortName,
		Port: backupSvcPort,
	})
	return nil
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
		SetOwnerReferences(kube.BaseOwnerReference(owner)).
		SetLabels(labels).
		SetSelector(labels).
		SetServiceType(mongoServiceDefinition.Type)

	serviceType := mongoServiceDefinition.Type
	switch serviceType {
	case corev1.ServiceTypeNodePort, corev1.ServiceTypeLoadBalancer:
		// Service will have a NodePort
		svcPort := corev1.ServicePort{TargetPort: intstr.FromInt(int(port))}
		svcPort.NodePort = mongoServiceDefinition.Port
		if mongoServiceDefinition.Port != 0 {
			svcPort.Port = mongoServiceDefinition.Port
		} else {
			svcPort.Port = port
		}
		svcBuilder.AddPort(&svcPort).SetClusterIP("")
	case corev1.ServiceTypeClusterIP:
		svcBuilder.SetPublishNotReadyAddresses(true).SetClusterIP("None")
		// Service will have a named Port
		svcBuilder.AddPort(&corev1.ServicePort{Port: int32(port), Name: "mongodb"})
	default:
		// Service will have a regular Port (unnamed)
		svcBuilder.AddPort(&corev1.ServicePort{Port: int32(port)})
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
