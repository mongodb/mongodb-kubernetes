package create

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status/pvc"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	mekoService "github.com/mongodb/mongodb-kubernetes/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/placeholders"
	enterprisests "github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

var (
	externalConnectivityPortName = "external-connectivity-port"
	internalConnectivityPortName = "internal-connectivity-port"
	backupPortName               = "backup-port"
	appLabelKey                  = "app"
)

// DatabaseInKubernetes creates (updates if it exists) the StatefulSet with its Service.
// It returns any errors coming from Kubernetes API.
func DatabaseInKubernetes(ctx context.Context, client kubernetesClient.Client, mdb mdbv1.MongoDB, sts appsv1.StatefulSet, config func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) error {
	opts := config(mdb)
	set, err := enterprisests.CreateOrUpdateStatefulset(ctx, client, mdb.Namespace, log, &sts)
	if err != nil {
		return err
	}

	// For mc-sharded, we create external services in the ShardedClusterReconcileHelper.reconcileServices method.
	if mdb.Spec.IsMultiCluster() && mdb.IsShardedCluster() {
		return nil
	}

	namespacedName := kube.ObjectKey(mdb.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, &mdb, &set.Spec.ServiceName, nil, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})

	// Adds Prometheus Port if Prometheus has been enabled.
	prom := mdb.GetPrometheus()
	if prom != nil {
		//nolint:gosec // suppressing integer overflow warning for int32(prom.GetPort())
		internalService.Spec.Ports = append(internalService.Spec.Ports, corev1.ServicePort{Port: int32(prom.GetPort()), Name: "prometheus"})
	}

	if err := mekoService.CreateOrUpdateService(ctx, client, internalService); err != nil {
		return err
	}

	for podNum := 0; podNum < mdb.GetSpec().Replicas(); podNum++ {
		namespacedName = kube.ObjectKey(mdb.Namespace, dns.GetExternalServiceName(set.Name, podNum))
		if mdb.Spec.ExternalAccessConfiguration == nil {
			if err := mekoService.DeleteServiceIfItExists(ctx, client, namespacedName); err != nil {
				return err
			}
		} else {
			if err := createExternalServices(ctx, client, mdb, opts, namespacedName, set, podNum, log); err != nil {
				return err
			}
		}
	}

	return nil
}

// HandlePVCResize handles the state machine of a PVC resize.
// Note: it modifies the desiredSTS.annotation to trigger a rolling restart later
// We leverage workflowStatus.WithAdditionalOptions(...) to merge/update/add to existing mdb.status.pvc
func HandlePVCResize(ctx context.Context, memberClient kubernetesClient.Client, desiredSts *appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {
	existingStatefulSet, stsErr := memberClient.GetStatefulSet(ctx, kube.ObjectKey(desiredSts.Namespace, desiredSts.Name))
	if stsErr != nil {
		// if we are here it means its first reconciling, we can skip the whole pvc state machine
		if apiErrors.IsNotFound(stsErr) {
			return workflow.OK()
		} else {
			return workflow.Failed(stsErr)
		}
	}

	pvcResizes := resourceStorageHasChanged(existingStatefulSet.Spec.VolumeClaimTemplates, desiredSts.Spec.VolumeClaimTemplates)

	increaseStorageOfAtLeastOnePVC := false
	// we have decreased the storage for at least one pvc, we do not support that
	for _, pvcResize := range pvcResizes {
		if pvcResize.resizeIndicator == 1 {
			log.Debug("Can't update the stateful set, as we cannot decrease the pvc size")
			return workflow.Failed(xerrors.Errorf("can't update pvc and statefulset to a smaller storage, from: %s - to:%s", pvcResize.from, pvcResize.to))
		}
		if pvcResize.resizeIndicator == -1 {
			log.Infof("Detected PVC size expansion; for pvc %s, from: %s to: %s", pvcResize.pvcName, pvcResize.from, pvcResize.to)
			increaseStorageOfAtLeastOnePVC = true
		}
	}

	// The sts claim has been increased (based on resourceChangeIndicator) for at least one PVC,
	// and we are not in the middle of a resize (that means pvcPhase is pvc.PhaseNoAction) for this statefulset.
	// This means we want to start one
	if increaseStorageOfAtLeastOnePVC {
		if err := enterprisests.AddPVCAnnotation(desiredSts); err != nil {
			return workflow.Failed(xerrors.Errorf("can't add pvc annotation, err: %s", err))
		}

		log.Infof("Detected PVC size expansion; patching all pvcs and increasing the size for sts: %s", desiredSts.Name)
		if err := resizePVCsStorage(ctx, memberClient, desiredSts, log); err != nil {
			return workflow.Failed(xerrors.Errorf("can't resize pvc, err: %s", err))
		}

		finishedResizing, err := hasFinishedResizing(ctx, memberClient, desiredSts)
		if err != nil {
			return workflow.Failed(err)
		}

		if finishedResizing {
			log.Info("PVCs finished resizing")
			log.Info("Deleting StatefulSet and orphan pods")
			// Cascade delete the StatefulSet
			deletePolicy := metav1.DeletePropagationOrphan
			if err := memberClient.Delete(ctx, desiredSts, client.PropagationPolicy(deletePolicy)); err != nil && !apiErrors.IsNotFound(err) {
				return workflow.Failed(xerrors.Errorf("error deleting sts, err: %s", err))
			}

			deletedIsStatefulset := checkStatefulsetIsDeleted(ctx, memberClient, desiredSts, 1*time.Second, log)

			if !deletedIsStatefulset {
				log.Info("deletion has not been reflected in kube yet, restarting the reconcile")
				return workflow.Pending("STS has been orphaned but not yet reflected in kubernetes. " +
					"Restarting the reconcile").WithAdditionalOptions(status.NewPVCsStatusOption(&status.PVC{Phase: pvc.PhasePVCResize, StatefulsetName: desiredSts.Name}))
			}
			log.Info("Statefulset have been orphaned")
			return workflow.OK().WithAdditionalOptions(status.NewPVCsStatusOption(&status.PVC{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: desiredSts.Name}))
		} else {
			log.Info("PVCs are still resizing, waiting until it has finished")
			return workflow.Pending("PVC resizes has not finished; current state of sts: %s: %s", desiredSts.Name, pvc.PhasePVCResize).WithAdditionalOptions(status.NewPVCsStatusOption(&status.PVC{Phase: pvc.PhasePVCResize, StatefulsetName: desiredSts.Name}))
		}
	}

	return workflow.OK()
}

func checkStatefulsetIsDeleted(ctx context.Context, memberClient kubernetesClient.Client, desiredSts *appsv1.StatefulSet, sleepDuration time.Duration, log *zap.SugaredLogger) bool {
	// After deleting the statefulset it can take seconds to be reflected in kubernetes.
	// In case it is still not reflected
	deletedIsStatefulset := false
	for i := 0; i < 3; i++ {
		time.Sleep(sleepDuration)
		_, stsErr := memberClient.GetStatefulSet(ctx, kube.ObjectKey(desiredSts.Namespace, desiredSts.Name))
		if apiErrors.IsNotFound(stsErr) {
			deletedIsStatefulset = true
			break
		} else {
			log.Info("Statefulset still exists, attempting again")
		}
	}
	return deletedIsStatefulset
}

func hasFinishedResizing(ctx context.Context, memberClient kubernetesClient.Client, desiredSts *appsv1.StatefulSet) (bool, error) {
	pvcList := corev1.PersistentVolumeClaimList{}
	if err := memberClient.List(ctx, &pvcList, client.InNamespace(desiredSts.Namespace)); err != nil {
		return false, err
	}

	finishedResizing := true
	for _, currentPVC := range pvcList.Items {
		if template, index := getMatchingPVCTemplateFromSTS(desiredSts, &currentPVC); template != nil {
			if currentPVC.Status.Capacity.Storage().Cmp(*desiredSts.Spec.VolumeClaimTemplates[index].Spec.Resources.Requests.Storage()) != 0 {
				finishedResizing = false
			}
		}
	}
	return finishedResizing, nil
}

// resizePVCsStorage takes the sts we want to create and update all matching pvc with the new storage
func resizePVCsStorage(ctx context.Context, kubeClient kubernetesClient.Client, statefulSetToCreate *appsv1.StatefulSet, log *zap.SugaredLogger) error {
	// this is to ensure that requests to a potentially not allowed resource is not blocking the operator until the end
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	pvcList := corev1.PersistentVolumeClaimList{}
	if err := kubeClient.List(ctx, &pvcList, client.InNamespace(statefulSetToCreate.Namespace)); err != nil {
		return err
	}

	for _, existingPVC := range pvcList.Items {
		if template, _ := getMatchingPVCTemplateFromSTS(statefulSetToCreate, &existingPVC); template != nil {
			currentSize := existingPVC.Spec.Resources.Requests[corev1.ResourceStorage]
			targetSize := *template.Spec.Resources.Requests.Storage()
			log.Infof("Resizing PVC %s/%s from %s to %s", existingPVC.GetNamespace(), existingPVC.GetName(), currentSize.String(), targetSize.String())
			existingPVC.Spec.Resources.Requests[corev1.ResourceStorage] = targetSize
			if err := kubeClient.Update(ctx, &existingPVC); err != nil {
				return err
			}
		}
	}
	return nil
}

func getMatchingPVCTemplateFromSTS(statefulSet *appsv1.StatefulSet, pvc *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, int) {
	for i, claimTemplate := range statefulSet.Spec.VolumeClaimTemplates {
		expectedPrefix := fmt.Sprintf("%s-%s", claimTemplate.Name, statefulSet.Name)

		// Regex to match expectedPrefix followed by a dash and a number (ordinal)
		regexPattern := fmt.Sprintf("^%s-[0-9]+$", regexp.QuoteMeta(expectedPrefix))
		if matched, _ := regexp.MatchString(regexPattern, pvc.Name); matched {
			return &claimTemplate, i
		}
	}
	return nil, -1
}

// createExternalServices creates the external services.
// For sharded clusters: services are only created for mongos.
func createExternalServices(ctx context.Context, client kubernetesClient.Client, mdb mdbv1.MongoDB, opts construct.DatabaseStatefulSetOptions, namespacedName client.ObjectKey, set *appsv1.StatefulSet, podNum int, log *zap.SugaredLogger) error {
	if mdb.IsShardedCluster() && !opts.IsMongos() {
		return nil
	}
	// TODO: we should not use OpsManager specific type `omv1.MongoDBOpsManagerServiceDefinition`
	externalService := BuildService(namespacedName, &mdb, &set.Spec.ServiceName, ptr.To(dns.GetPodName(set.Name, podNum)), opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeLoadBalancer})

	if mdb.Spec.DbCommonSpec.GetExternalDomain() != nil {
		// When an external domain is defined, we put it into process.hostname in automation config. Because of that we need to define additional well-defined port for backups.
		// This backup port is not needed when we use headless service, because then agent is resolving DNS directly to pod's IP and that allows to connect
		// to any port in a pod, even ephemeral one.
		// When we put any other address than headless service into process.hostname: non-headless service fqdn (e.g. in multi cluster using service mesh) or
		// external domain (e.g. for multi-cluster no-mesh), then we need to define backup port.
		// In the agent process, we pass -ephemeralPortOffset 1 argument to define, that backup port should be a standard port+1.
		backupPort := GetNonEphemeralBackupPort(opts.ServicePort)
		externalService.Spec.Ports = append(externalService.Spec.Ports, corev1.ServicePort{Port: backupPort, TargetPort: intstr.FromInt32(backupPort), Name: "backup"})
	}

	if mdb.Spec.ExternalAccessConfiguration.ExternalService.SpecWrapper != nil {
		externalService.Spec = merge.ServiceSpec(externalService.Spec, mdb.Spec.ExternalAccessConfiguration.ExternalService.SpecWrapper.Spec)
	}
	externalService.Annotations = merge.StringToStringMap(externalService.Annotations, mdb.Spec.ExternalAccessConfiguration.ExternalService.Annotations)

	placeholderReplacer := GetSingleClusterMongoDBPlaceholderReplacer(mdb.Name, set.Name, mdb.Namespace, mdb.ServiceName(), mdb.Spec.GetExternalDomain(), mdb.Spec.GetClusterDomain(), podNum, mdb.GetResourceType())
	if processedAnnotations, replacedFlag, err := placeholderReplacer.ProcessMap(externalService.Annotations); err != nil {
		return xerrors.Errorf("failed to process annotations in service %s: %w", externalService.Name, err)
	} else if replacedFlag {
		log.Debugf("Replaced placeholders in annotations in external service: %s. Annotations before: %+v, annotations after: %+v", externalService.Name, externalService.Annotations, processedAnnotations)
		externalService.Annotations = processedAnnotations
	}

	err := mekoService.CreateOrUpdateService(ctx, client, externalService)
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
	PlaceholderMongosProcessDomain = "mongosProcessDomain"
	PlaceholderMongodProcessDomain = "mongodProcessDomain"
	PlaceholderMongosProcessFQDN   = "mongosProcessFQDN"
	PlaceholderMongodProcessFQDN   = "mongodProcessFQDN"
	PlaceholderClusterName         = "clusterName"
	PlaceholderClusterIndex        = "clusterIndex"
)

func GetSingleClusterMongoDBPlaceholderReplacer(resourceName string, statefulSetName string, namespace string, serviceName string, externalDomain *string, clusterDomain string, podIdx int, resourceType mdbv1.ResourceType) *placeholders.Replacer {
	podName := dns.GetPodName(statefulSetName, podIdx)
	placeholderValues := map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIdx),
		PlaceholderNamespace:           namespace,
		PlaceholderResourceName:        resourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     statefulSetName,
		PlaceholderExternalServiceName: dns.GetExternalServiceName(statefulSetName, podIdx),
	}

	if resourceType == mdbv1.ShardedCluster {
		placeholderValues[PlaceholderMongosProcessDomain] = dns.GetServiceFQDN(serviceName, namespace, clusterDomain)
		placeholderValues[PlaceholderMongosProcessFQDN] = dns.GetPodFQDN(podName, serviceName, namespace, clusterDomain, externalDomain)
		if externalDomain != nil {
			placeholderValues[PlaceholderMongosProcessDomain] = *externalDomain
		}
	} else {
		placeholderValues[PlaceholderMongodProcessDomain] = dns.GetServiceFQDN(serviceName, namespace, clusterDomain)
		placeholderValues[PlaceholderMongodProcessFQDN] = dns.GetPodFQDN(podName, serviceName, namespace, clusterDomain, externalDomain)
		if externalDomain != nil {
			placeholderValues[PlaceholderMongodProcessDomain] = *externalDomain
		}
	}

	return placeholders.New(placeholderValues)
}

func GetMultiClusterMongoDBPlaceholderReplacer(name string, stsName string, namespace string, clusterName string, clusterNum int, externalDomain *string, clusterDomain string, podIdx int) *placeholders.Replacer {
	podName := dns.GetMultiPodName(stsName, clusterNum, podIdx)
	serviceDomain := dns.GetServiceDomain(namespace, clusterDomain, externalDomain)
	placeholderValues := map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIdx),
		PlaceholderNamespace:           namespace,
		PlaceholderResourceName:        name,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     dns.GetMultiStatefulSetName(stsName, clusterNum),
		PlaceholderExternalServiceName: dns.GetMultiExternalServiceName(stsName, clusterNum, podIdx),
		PlaceholderMongodProcessDomain: serviceDomain,
		PlaceholderMongodProcessFQDN:   dns.GetMultiClusterPodServiceFQDN(stsName, namespace, clusterNum, externalDomain, podIdx, clusterDomain),
		PlaceholderClusterName:         clusterName,
		PlaceholderClusterIndex:        fmt.Sprintf("%d", clusterNum),
	}

	if strings.HasSuffix(stsName, "mongos") {
		placeholderValues[PlaceholderMongosProcessDomain] = serviceDomain
		placeholderValues[PlaceholderMongosProcessFQDN] = dns.GetMultiClusterPodServiceFQDN(stsName, namespace, clusterNum, externalDomain, podIdx, clusterDomain)
		if externalDomain != nil {
			placeholderValues[PlaceholderMongosProcessDomain] = *externalDomain
		}
	} else {
		placeholderValues[PlaceholderMongodProcessDomain] = serviceDomain
		placeholderValues[PlaceholderMongodProcessFQDN] = dns.GetMultiClusterPodServiceFQDN(stsName, namespace, clusterNum, externalDomain, podIdx, clusterDomain)
		if externalDomain != nil {
			placeholderValues[PlaceholderMongodProcessDomain] = *externalDomain
		}
	}

	return placeholders.New(placeholderValues)
}

// AppDBInKubernetes creates or updates the StatefulSet and Service required for the AppDB.
func AppDBInKubernetes(ctx context.Context, client kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager, sts appsv1.StatefulSet, serviceSelectorLabel string, log *zap.SugaredLogger) error {
	set, err := enterprisests.CreateOrUpdateStatefulset(ctx, client, opsManager.Namespace, log, &sts)
	if err != nil {
		return err
	}

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, opsManager, ptr.To(serviceSelectorLabel), nil, opsManager.Spec.AppDB.AdditionalMongodConfig.GetPortOrDefault(), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})

	// Adds Prometheus Port if Prometheus has been enabled.
	prom := opsManager.Spec.AppDB.Prometheus
	if prom != nil {
		//nolint:gosec // suppressing integer overflow warning for int32(prom.GetPort())
		internalService.Spec.Ports = append(internalService.Spec.Ports, corev1.ServicePort{Port: int32(prom.GetPort()), Name: "prometheus"})
	}

	return mekoService.CreateOrUpdateService(ctx, client, internalService)
}

// BackupDaemonInKubernetes creates or updates the StatefulSet and Services required.
func BackupDaemonInKubernetes(ctx context.Context, client kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) (bool, error) {
	set, err := enterprisests.CreateOrUpdateStatefulset(ctx, client, opsManager.Namespace, log, &sts)
	if err != nil {
		// Check if it is a k8s error or a custom one
		var statefulSetCantBeUpdatedError enterprisests.StatefulSetCantBeUpdatedError
		if !errors.As(err, &statefulSetCantBeUpdatedError) {
			return false, err
		}

		// In this case, we delete the old Statefulset
		log.Debug("Deleting the old backup stateful set and creating a new one")
		stsNamespacedName := kube.ObjectKey(sts.Namespace, sts.Name)
		err = client.DeleteStatefulSet(ctx, stsNamespacedName)
		if err != nil {
			return false, xerrors.Errorf("failed while trying to delete previous backup daemon statefulset: %w", err)
		}
		return true, nil
	}
	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, opsManager, &set.Spec.ServiceName, nil, construct.BackupDaemonServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = mekoService.CreateOrUpdateService(ctx, client, internalService)
	return false, err
}

// OpsManagerInKubernetes creates all of the required Kubernetes resources for Ops Manager.
// It creates the StatefulSet and all required services.
func OpsManagerInKubernetes(ctx context.Context, memberCluster multicluster.MemberCluster, opsManager *omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) error {
	set, err := enterprisests.CreateOrUpdateStatefulset(ctx, memberCluster.Client, opsManager.Namespace, log, &sts)
	if err != nil {
		return err
	}

	_, port := opsManager.GetSchemePort()

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, opsManager, &set.Spec.ServiceName, nil, port, getInternalServiceDefinition(opsManager))

	// add queryable backup port to service
	if opsManager.Spec.Backup.Enabled {
		if err := addQueryableBackupPortToService(opsManager, &internalService, internalConnectivityPortName); err != nil {
			return err
		}
	}

	if err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, internalService); err != nil {
		return err
	}

	namespacedName = kube.ObjectKey(opsManager.Namespace, opsManager.ExternalSvcName())
	if externalConnectivity := opsManager.GetExternalConnectivityConfigurationForMemberCluster(memberCluster.Name); externalConnectivity != nil {
		svc := BuildService(namespacedName, opsManager, &set.Spec.ServiceName, nil, port, *externalConnectivity)

		// Need to create queryable backup service
		if opsManager.Spec.Backup.Enabled {
			if err := addQueryableBackupPortToService(opsManager, &svc, externalConnectivityPortName); err != nil {
				return err
			}
		}

		if err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc); err != nil {
			return err
		}
	} else {
		if err := mekoService.DeleteServiceIfItExists(ctx, memberCluster.Client, namespacedName); err != nil {
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
		serviceDefinition.ClusterIP = ptr.To("")
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
func BuildService(namespacedName types.NamespacedName, owner v1.ObjectOwner, appLabel *string, podLabel *string, port int32, mongoServiceDefinition omv1.MongoDBOpsManagerServiceDefinition) corev1.Service {
	svcLabels := owner.GetOwnerLabels()

	selectorLabels := map[string]string{
		util.OperatorLabelName: util.OperatorLabelValue,
	}

	if appLabel != nil {
		svcLabels[appLabelKey] = *appLabel
		selectorLabels[appLabelKey] = *appLabel
	}

	if podLabel != nil {
		svcLabels[appsv1.StatefulSetPodNameLabel] = *podLabel
		selectorLabels[appsv1.StatefulSetPodNameLabel] = *podLabel
	}

	svcBuilder := service.Builder().
		SetNamespace(namespacedName.Namespace).
		SetName(namespacedName.Name).
		SetOwnerReferences(kube.BaseOwnerReference(owner)).
		SetLabels(svcLabels).
		SetSelector(selectorLabels).
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

type pvcResize struct {
	pvcName         string
	resizeIndicator int
	from            string
	to              string
}

// resourceStorageHasChanged returns 0 if both storage sizes are equal or not exist at all,
//
//	 1: toCreateVolumeClaims < desiredVolumeClaims → decrease storage
//	-1: toCreateVolumeClaims > desiredVolumeClaims → increase storage
//	 0: toCreateVolumeClaims = desiredVolumeClaims → storage stays same
func resourceStorageHasChanged(existingVolumeClaims []corev1.PersistentVolumeClaim, desiredVolumeClaims []corev1.PersistentVolumeClaim) []pvcResize {
	existingClaimByName := map[string]*corev1.PersistentVolumeClaim{}
	var pvcResizes []pvcResize

	for _, existingClaim := range existingVolumeClaims {
		existingClaimByName[existingClaim.Name] = &existingClaim
	}

	for _, desiredClaim := range desiredVolumeClaims {
		// if the desiredClaim does not exist in the list of claims, then we don't need to consider resizing, since
		// its most likely a new one
		if existingPVCClaim, ok := existingClaimByName[desiredClaim.Name]; ok {
			desiredPVCClaimStorage := desiredClaim.Spec.Resources.Requests.Storage()
			existingPVCClaimStorage := existingPVCClaim.Spec.Resources.Requests.Storage()
			if desiredPVCClaimStorage != nil && existingPVCClaimStorage != nil {
				pvcResizes = append(pvcResizes, pvcResize{
					pvcName:         desiredClaim.Name,
					resizeIndicator: existingPVCClaimStorage.Cmp(*desiredPVCClaimStorage),
					from:            existingPVCClaimStorage.String(),
					to:              desiredPVCClaimStorage.String(),
				})
			}
		}
	}

	return pvcResizes
}
