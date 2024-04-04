package create

import (
	"errors"
	"fmt"

	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"

	mekoService "github.com/10gen/ops-manager-kubernetes/pkg/kube/service"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/placeholders"
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
	internalService := BuildService(namespacedName, &mdb, &set.Spec.ServiceName, nil, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})

	// Adds Prometheus Port if Prometheus has been enabled.
	prom := mdb.GetPrometheus()
	if prom != nil {
		internalService.Spec.Ports = append(internalService.Spec.Ports, corev1.ServicePort{Port: int32(prom.GetPort()), Name: "prometheus"})
	}
	err = mekoService.CreateOrUpdateService(client, internalService)
	if err != nil {
		return err
	}

	for podNum := 0; podNum < mdb.GetSpec().Replicas(); podNum++ {
		namespacedName = kube.ObjectKey(mdb.Namespace, dns.GetExternalServiceName(set.Name, podNum))
		if mdb.Spec.ExternalAccessConfiguration == nil {
			if err := mekoService.DeleteServiceIfItExists(client, namespacedName); err != nil {
				return err
			}
			continue
		}

		if mdb.Spec.ExternalAccessConfiguration != nil {
			// we only need an external service for mongos
			if err = createExternalServices(client, mdb, opts, namespacedName, set, podNum, log); err != nil {
				return err
			}
		}
	}

	return nil
}

// createExternalServices creates the external services. The function does not create external services for sharded clusters which given stateful-sets are not mongos.
func createExternalServices(client kubernetesClient.Client, mdb mdbv1.MongoDB, opts construct.DatabaseStatefulSetOptions, namespacedName client.ObjectKey, set *appsv1.StatefulSet, podNum int, log *zap.SugaredLogger) error {
	if mdb.IsShardedCluster() && !opts.IsMongos() {
		return nil
	}
	externalService := BuildService(namespacedName, &mdb, &set.Spec.ServiceName, pointer.String(dns.GetPodName(set.Name, podNum)), opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeLoadBalancer})

	if mdb.Spec.DbCommonSpec.GetExternalDomain() != nil {
		// When an external domain is defined, we put it into process.hostname in automation config. Because of that we need to define additional well-defined port for backups.
		// This backup port is not needed when we use headless service, because then agent is resolving DNS directly to pod's IP and that allows to connect
		// to any port in a pod, even ephemeral one.
		// When we put any other address than headless service into process.hostname: non-headless service fqdn (e.g. in multi cluster using service mesh) or
		// external domain (e.g. for multi-cluster no-mesh), then we need to define backup port.
		// In the agent process, we pass -ephemeralPortOffset 1 argument to define, that backup port should be a standard port+1.
		backupPort := GetNonEphemeralBackupPort(opts.ServicePort)
		externalService.Spec.Ports = append(externalService.Spec.Ports, corev1.ServicePort{Port: backupPort, TargetPort: intstr.FromInt(int(backupPort)), Name: "backup"})
	}

	if mdb.Spec.DbCommonSpec.ExternalAccessConfiguration.ExternalService.SpecWrapper != nil {
		externalService.Spec = merge.ServiceSpec(externalService.Spec, mdb.Spec.DbCommonSpec.ExternalAccessConfiguration.ExternalService.SpecWrapper.Spec)
	}
	externalService.Annotations = merge.StringToStringMap(externalService.Annotations, mdb.Spec.ExternalAccessConfiguration.ExternalService.Annotations)

	placeholderReplacer := GetSingleClusterMongoDBPlaceholderReplacer(mdb.Name, mdb.Namespace, mdb.ServiceName(), &mdb.Spec, podNum)
	if processedAnnotations, replacedFlag, err := placeholderReplacer.ProcessMap(externalService.Annotations); err != nil {
		return xerrors.Errorf("failed to process annotations in service %s: %w", externalService.Name, err)
	} else if replacedFlag {
		log.Debugf("Replaced placeholders in annotations in external service: %s. Annotations before: %+v, annotations after: %+v", externalService.Name, externalService.Annotations, processedAnnotations)
		externalService.Annotations = processedAnnotations
	}

	err := mekoService.CreateOrUpdateService(client, externalService)
	if err != nil && !apiErrors.IsAlreadyExists(err) {
		return xerrors.Errorf("failed to created external service: %s, err: %w", externalService.Name, err)
	}
	return nil
}

const (
	PlaceholderPodIndex            = "podIndex"
	PlaceholderNamespace           = "namespace"
	PlaceholderResourceName        = "resourceName"
	PlaceholderPodName             = "podName"
	PlaceholderStatefulSetName     = "statefulSetName"
	PlaceholderExternalServiceName = "externalServiceName"
	PlaceholderMongodProcessDomain = "mongodProcessDomain"
	PlaceholderMongodProcessFQDN   = "mongodProcessFQDN"
	PlaceholderClusterName         = "clusterName"
	PlaceholderClusterIndex        = "clusterIndex"
)

func GetSingleClusterMongoDBPlaceholderReplacer(name string, namespace string, serviceName string, dbSpec mdbv1.DbSpec, podIdx int) *placeholders.Replacer {
	podName := dns.GetPodName(name, podIdx)
	placeholderValues := map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIdx),
		PlaceholderNamespace:           namespace,
		PlaceholderResourceName:        name,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     name,
		PlaceholderExternalServiceName: dns.GetExternalServiceName(name, podIdx),
		PlaceholderMongodProcessDomain: dns.GetServiceFQDN(serviceName, namespace, dbSpec.GetClusterDomain()),
		PlaceholderMongodProcessFQDN:   dns.GetPodFQDN(podName, serviceName, namespace, dbSpec.GetClusterDomain(), dbSpec.GetExternalDomain()),
	}

	if dbSpec.GetExternalDomain() != nil {
		placeholderValues[PlaceholderMongodProcessDomain] = *dbSpec.GetExternalDomain()
	}

	return placeholders.New(placeholderValues)
}

func GetMultiClusterMongoDBPlaceholderReplacer(name string, namespace string, clusterName string, clusterNum int, mdbmc *mdbmultiv1.MongoDBMultiCluster, podIdx int) *placeholders.Replacer {
	podName := dns.GetMultiPodName(name, clusterNum, podIdx)
	externalDomain := mdbmc.Spec.GetExternalDomainForMemberCluster(clusterName)
	serviceDomain := dns.GetServiceDomain(namespace, mdbmc.Spec.GetClusterDomain(), externalDomain)
	placeholderValues := map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIdx),
		PlaceholderNamespace:           namespace,
		PlaceholderResourceName:        name,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     dns.GetMultiStatefulSetName(name, clusterNum),
		PlaceholderExternalServiceName: dns.GetMultiExternalServiceName(name, clusterNum, podIdx),
		PlaceholderMongodProcessDomain: serviceDomain,
		PlaceholderMongodProcessFQDN:   dns.GetMultiClusterPodServiceFQDN(name, namespace, clusterNum, externalDomain, podIdx, mdbmc.Spec.GetClusterDomain()),
		PlaceholderClusterName:         clusterName,
		PlaceholderClusterIndex:        fmt.Sprintf("%d", clusterNum),
	}

	return placeholders.New(placeholderValues)
}

// AppDBInKubernetes creates or updates the StatefulSet and Service required for the AppDB.
func AppDBInKubernetes(client kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager, sts appsv1.StatefulSet, serviceSelectorLabel string, log *zap.SugaredLogger) error {

	set, err := enterprisests.CreateOrUpdateStatefulset(client,
		opsManager.Namespace,
		log,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, opsManager, pointer.String(serviceSelectorLabel), nil, opsManager.Spec.AppDB.AdditionalMongodConfig.GetPortOrDefault(), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})

	// Adds Prometheus Port if Prometheus has been enabled.
	prom := opsManager.Spec.AppDB.Prometheus
	if prom != nil {
		internalService.Spec.Ports = append(internalService.Spec.Ports, corev1.ServicePort{Port: int32(prom.GetPort()), Name: "prometheus"})
	}

	return mekoService.CreateOrUpdateService(client, internalService)
}

// BackupDaemonInKubernetes creates or updates the StatefulSet and Services required.
func BackupDaemonInKubernetes(client kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) (bool, error) {
	set, err := enterprisests.CreateOrUpdateStatefulset(
		client,
		opsManager.Namespace,
		log,
		&sts,
	)

	if err != nil {
		// Check if it is a k8s error or a custom one
		var statefulSetCantBeUpdatedError enterprisests.StatefulSetCantBeUpdatedError
		if !errors.As(err, &statefulSetCantBeUpdatedError) {
			return false, err
		}

		// In this case, we delete the old Statefulset
		log.Debug("Deleting the old backup stateful set and creating a new one")
		stsNamespacedName := kube.ObjectKey(sts.Namespace, sts.Name)
		err = client.DeleteStatefulSet(stsNamespacedName)
		if err != nil {
			return false, xerrors.Errorf("failed while trying to delete previous backup daemon statefulset: %w", err)
		}
		return true, nil
	}
	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, opsManager, &set.Spec.ServiceName, nil, construct.BackupDaemonServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = mekoService.CreateOrUpdateService(client, internalService)
	return false, err
}

// OpsManagerInKubernetes creates all of the required Kubernetes resources for Ops Manager.
// It creates the StatefulSet and all required services.
func OpsManagerInKubernetes(client kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) error {
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
	internalService := BuildService(namespacedName, opsManager, &set.Spec.ServiceName, nil, int32(port), getInternalServiceDefinition(opsManager))

	// add queryable backup port to service
	if opsManager.Spec.Backup.Enabled {
		if err := addQueryableBackupPortToService(opsManager, &internalService, internalConnectivityPortName); err != nil {
			return err
		}
	}

	err = mekoService.CreateOrUpdateService(client, internalService)
	if err != nil {
		return err
	}

	namespacedName = kube.ObjectKey(opsManager.Namespace, opsManager.ExternalSvcName())
	var externalService *corev1.Service = nil
	if opsManager.Spec.MongoDBOpsManagerExternalConnectivity != nil {
		svc := BuildService(namespacedName, opsManager, &set.Spec.ServiceName, nil, int32(port), *opsManager.Spec.MongoDBOpsManagerExternalConnectivity)
		externalService = &svc
	} else {
		if err := mekoService.DeleteServiceIfItExists(client, namespacedName); err != nil {
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
		if err := mekoService.CreateOrUpdateService(client, *externalService); err != nil {
			return err
		}
	}
	return nil
}

func getInternalServiceDefinition(opsManager *omv1.MongoDBOpsManager) omv1.MongoDBOpsManagerServiceDefinition {
	if opsManager.Spec.InternalConnectivity != nil {
		return *opsManager.Spec.InternalConnectivity
	}
	serviceDefinition := omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP}
	// For multicluster OpsManager we create ClusterIP services by default, based on the assumption
	// that the multi-cluster architecture is a multi-network configuration in most cases,
	// which makes headless services not resolve across different clusters.
	// https://github.com/istio/istio/issues/36733
	// The Spec.InternalConnectivity field allows for explicitly configuring headless services
	// and adding additional annotations to the service used for internal connectivity.
	if opsManager.Spec.IsMultiCluster() {
		serviceDefinition.ClusterIP = pointer.String("")
	}
	return serviceDefinition
}

// addQueryableBackupPortToService adds the backup port to the existing external Ops Manager service.
// this function assumes externalService is not nil.
func addQueryableBackupPortToService(opsManager *omv1.MongoDBOpsManager, service *corev1.Service, portName string) error {
	backupSvcPort, err := opsManager.Spec.BackupDaemonSvcPort()
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

// BuildService creates the Kube Service. If it should be seen externally it makes it of type NodePort that will assign
// some random port in the range 30000-32767
// Note that itself service has no dedicated IP by default ("clusterIP: None") as all mongo entities should be directly
// addressable.
// This function will update a Service object if passed, or return a new one if passed nil, this is to be able to update
// Services and to not change any attribute they might already have that needs to be maintained.
//
// When appLabel is specified, then the selector is targeting all pods (round-robin service). Usable for e.g. OpsManager service.
// When podLabel is specified, then the selector is targeting only a single pod. Used for external services or multi-cluster services.
func BuildService(namespacedName types.NamespacedName, owner v1.CustomResourceReadWriter, appLabel *string, podLabel *string, port int32, mongoServiceDefinition omv1.MongoDBOpsManagerServiceDefinition) corev1.Service {
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
	case corev1.ServiceTypeNodePort:
		// Service will have a NodePort
		svcPort := corev1.ServicePort{TargetPort: intstr.FromInt(int(port)), Name: "mongodb"}
		svcPort.NodePort = mongoServiceDefinition.Port
		if mongoServiceDefinition.Port != 0 {
			svcPort.Port = mongoServiceDefinition.Port
		} else {
			svcPort.Port = port
		}
		svcBuilder.AddPort(&svcPort).SetClusterIP("")
	case corev1.ServiceTypeLoadBalancer:
		svcPort := corev1.ServicePort{TargetPort: intstr.FromInt(int(port)), Name: "mongodb"}
		if mongoServiceDefinition.Port != 0 {
			svcPort.Port = mongoServiceDefinition.Port
		} else {
			svcPort.Port = port
		}
		svcBuilder.AddPort(&svcPort).SetClusterIP("")
	case corev1.ServiceTypeClusterIP:
		if mongoServiceDefinition.ClusterIP == nil {
			svcBuilder.SetClusterIP("None")
		} else {
			svcBuilder.SetClusterIP(*mongoServiceDefinition.ClusterIP)
		}
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
