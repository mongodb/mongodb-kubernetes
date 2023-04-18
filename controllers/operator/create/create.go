package create

import (
	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	enterprisests "github.com/10gen/ops-manager-kubernetes/pkg/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	appsv1 "k8s.io/api/apps/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var (
	externalConnectivityPortName = "external-connectivity-port"
	internalConnectivityPortName = "internal-connectivity-port"
	backupPortName               = "backup-port"
	appLabelKey                  = "app"
	podNameLabelKey              = "statefulset.kubernetes.io/pod-name"
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
	internalService := buildService(namespacedName, &mdb, &set.Spec.ServiceName, nil, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})

	// Adds Prometheus Port if Prometheus has been enabled.
	prom := mdb.GetPrometheus()
	if prom != nil {
		internalService.Spec.Ports = append(internalService.Spec.Ports, corev1.ServicePort{Port: int32(prom.GetPort()), Name: "prometheus"})
	}
	err = service.CreateOrUpdateService(client, internalService)
	if err != nil {
		return err
	}

	for podNum := 0; podNum < mdb.GetSpec().Replicas(); podNum++ {
		namespacedName = kube.ObjectKey(mdb.Namespace, dns.GetExternalServiceName(set.Name, podNum))
		if mdb.Spec.ExternalAccessConfiguration == nil {
			if err := service.DeleteServiceIfItExists(client, namespacedName); err != nil {
				return err
			}
			continue
		}

		if mdb.Spec.ExternalAccessConfiguration != nil {
			// we only need an external service for mongos
			if err = createExternalServices(client, mdb, opts, namespacedName, set, podNum); err != nil {
				return err
			}
		}
	}

	return nil
}

// createExternalServices creates the external services. The function does not create external services for sharded clusters which given stateful-sets are not mongos.
func createExternalServices(client kubernetesClient.Client, mdb mdbv1.MongoDB, opts construct.DatabaseStatefulSetOptions, namespacedName client.ObjectKey, set *appsv1.StatefulSet, podNum int) error {
	if mdb.IsShardedCluster() && !opts.IsMongos() {
		return nil
	}
	externalService := buildService(namespacedName, &mdb, &set.Spec.ServiceName, pointer.String(dns.GetPodName(set.Name, podNum)), opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeLoadBalancer})

	if mdb.Spec.DbCommonSpec.GetExternalDomain() != nil {
		// When an external domain is defined, we put it into process.hostname in automation config. Because of that we need to define additional well-defined port for backups.
		// This backup port is not needed when we use headless service, because then agent is resolving DNS directly to pod's IP and that allows to connect
		// to any port in a pod, even ephemeral one.
		// When we put any other address than headless service into process.hostname: non-headles service fqdn (e.g. in multi cluster using service mesh) or
		// external domain (e.g. for multi-cluster no-mesh), then we need to define backup port.
		// In the agent process, we pass -ephemeralPortOffset 1 argument to define, that backup port should be a starndard port+1.
		backupPort := GetNonEphemeralBackupPort(opts.ServicePort)
		externalService.Spec.Ports = append(externalService.Spec.Ports, corev1.ServicePort{Port: backupPort, TargetPort: intstr.FromInt(int(backupPort)), Name: "backup"})
	}

	if mdb.Spec.DbCommonSpec.ExternalAccessConfiguration.ExternalService.SpecWrapper != nil {
		externalService.Spec = merge.ServiceSpec(externalService.Spec, mdb.Spec.DbCommonSpec.ExternalAccessConfiguration.ExternalService.SpecWrapper.Spec)
	}
	externalService.Annotations = merge.StringToStringMap(externalService.Annotations, mdb.Spec.ExternalAccessConfiguration.ExternalService.Annotations)

	err := service.CreateOrUpdateService(client, externalService)
	if err != nil && !apiErrors.IsAlreadyExists(err) {
		return xerrors.Errorf("failed to created external service: %s, err: %w", externalService.Name, err)
	}
	return nil
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
	internalService := buildService(namespacedName, &opsManager, &set.Spec.ServiceName, nil, opsManager.Spec.AppDB.AdditionalMongodConfig.GetPortOrDefault(), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})

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
			return false, xerrors.Errorf("failed while trying to delete previous backup daemon statefulset: %w", err)
		}
		return true, nil
	}
	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &opsManager, &set.Spec.ServiceName, nil, construct.BackupDaemonServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
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
	internalService := buildService(namespacedName, &opsManager, &set.Spec.ServiceName, nil, int32(port), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
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
		svc := buildService(namespacedName, &opsManager, &set.Spec.ServiceName, nil, int32(port), *opsManager.Spec.MongoDBOpsManagerExternalConnectivity)

		svc.Spec.Ports = append(svc.Spec.Ports)
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
		return xerrors.Errorf("can't parse queryable backup port: %w", err)
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
//
// When appLabel is specified, then the selector is targeting all pods (round-robin service). Usable for e.g. OpsManager service.
// When podLabel is specified, then the selector is targeting only a single pod. Used for external services or multi-cluster services.
func buildService(namespacedName types.NamespacedName, owner v1.CustomResourceReadWriter, appLabel *string, podLabel *string, port int32, mongoServiceDefinition omv1.MongoDBOpsManagerServiceDefinition) corev1.Service {
	labels := map[string]string{
		construct.ControllerLabelName: util.OperatorName,
	}

	if appLabel != nil {
		labels[appLabelKey] = *appLabel
	}

	if podLabel != nil {
		labels[podNameLabelKey] = *podLabel
	}

	svcBuilder := service.Builder().
		SetNamespace(namespacedName.Namespace).
		SetName(namespacedName.Name).
		SetOwnerReferences(kube.BaseOwnerReference(owner)).
		SetLabels(labels).
		SetSelector(labels).
		SetServiceType(mongoServiceDefinition.Type).
		SetPublishNotReadyAddresses(true)

	serviceType := mongoServiceDefinition.Type
	switch serviceType {
	case corev1.ServiceTypeNodePort, corev1.ServiceTypeLoadBalancer:
		// Service will have a NodePort
		svcPort := corev1.ServicePort{TargetPort: intstr.FromInt(int(port)), Name: "mongodb"}
		svcPort.NodePort = mongoServiceDefinition.Port
		if mongoServiceDefinition.Port != 0 {
			svcPort.Port = mongoServiceDefinition.Port
		} else {
			svcPort.Port = port
		}
		svcBuilder.AddPort(&svcPort).SetClusterIP("")
	case corev1.ServiceTypeClusterIP:
		svcBuilder.SetClusterIP("None")
		// Service will have a named Port
		svcBuilder.AddPort(&corev1.ServicePort{Port: port, Name: "mongodb"})
	default:
		// Service will have a regular Port (unnamed)
		svcBuilder.AddPort(&corev1.ServicePort{Port: port})
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

// GetNonEphemeralBackupPort returns port number that will be used when we instruct the agent to use non-ephemeral port for backup monogod.
// Non-ephemeral ports are used when we set process.hostname for anything other than headless service FQDN.
func GetNonEphemeralBackupPort(mongodPort int32) int32 {
	return mongodPort + 1
}
