package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster/memberwatch"
	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	enterprisests "github.com/10gen/ops-manager-kubernetes/pkg/statefulset"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/hashicorp/go-multierror"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/process"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	mconstruct "github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/multicluster"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	khandler "github.com/10gen/ops-manager-kubernetes/pkg/handler"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileMongoDbMultiReplicaSet reconciles a MongoDB ReplicaSet across multiple Kubernetes clusters
type ReconcileMongoDbMultiReplicaSet struct {
	*ReconcileCommonController
	omConnectionFactory           om.ConnectionFactory
	memberClusterClientsMap       map[string]kubernetesClient.Client // holds the client for each of the memberclusters(where the MongoDB ReplicaSet is deployed)
	memberClusterSecretClientsMap map[string]secrets.SecretClient
}

var _ reconcile.Reconciler = &ReconcileMongoDbMultiReplicaSet{}

func newMultiClusterReplicaSetReconciler(mgr manager.Manager, omFunc om.ConnectionFactory, memberClustersMap map[string]cluster.Cluster) *ReconcileMongoDbMultiReplicaSet {
	clientsMap := make(map[string]kubernetesClient.Client)
	secretClientsMap := make(map[string]secrets.SecretClient)

	// extract client from each cluster object.
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v.GetClient())
		secretClientsMap[k] = secrets.SecretClient{
			VaultClient: nil, // Vault is not supported yet on multicluster
			KubeClient:  clientsMap[k],
		}
	}

	return &ReconcileMongoDbMultiReplicaSet{
		ReconcileCommonController:     newReconcileCommonController(mgr),
		omConnectionFactory:           omFunc,
		memberClusterClientsMap:       clientsMap,
		memberClusterSecretClientsMap: secretClientsMap,
	}
}

// MongoDBMulti Resource
// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbmulti,mongodbmulti/status,mongodbmulti/finalizers},verbs=*,namespace=placeholder

// Reconcile reads that state of the cluster for a MongoDbMultiReplicaSet object and makes changes based on the state read
// and what is in the MongoDbMultiReplicaSet.Spec
func (r *ReconcileMongoDbMultiReplicaSet) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	agents.UpgradeAllIfNeeded(r.client, r.SecretClient, r.omConnectionFactory, GetWatchedNamespace())

	log := zap.S().With("MultiReplicaSet", request.NamespacedName)
	log.Info("-> MultiReplicaSet.Reconcile")

	// Fetch the MongoDBMulti instance
	mrs := mdbmultiv1.MongoDBMulti{}
	if reconcileResult, err := r.prepareResourceForReconciliation(request, &mrs, log); err != nil {
		if apiErrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		log.Errorf("error preparing resource for reconciliation: %s", err)
		return reconcileResult, err
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, &mrs, log)
	if err != nil {
		return r.updateStatus(&mrs, workflow.Failed("Error reading project config and credentials: %s", err), log)
	}

	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, mrs.Namespace, log)
	if err != nil {
		return r.updateStatus(&mrs, workflow.Failed("error establishing connection to Ops Manager: %s", err), log)
	}

	log = log.With("MemberCluster Namespace", mrs.Namespace)

	// check if resource has failedCluster annotation and mark it as failed if automated failover is not enabled
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		return r.updateStatus(&mrs, workflow.Failed(err.Error()), log)
	}
	if len(failedClusterNames) > 0 && !multicluster.ShouldPerformFailover() {
		return r.updateStatus(&mrs, workflow.Failed("resource has failed clusters in the annotation: %+v", failedClusterNames), log)
	}

	// register for the cert secrets and configmap to be watched
	if mrs.Spec.GetSecurity().IsTLSEnabled() {
		r.RegisterWatchedTLSResources(mrs.ObjectKey(), mrs.Spec.GetSecurity().TLSConfig.CA,
			[]string{mrs.Spec.GetSecurity().MemberCertificateSecretName(mrs.Name)})
	}

	needToPublishStateFirst, err := r.needToPublishStateFirstMultiCluster(&mrs, log)
	if err != nil {
		return r.updateStatus(&mrs, workflow.Failed(err.Error()), log)
	}

	status := workflow.RunInGivenOrder(needToPublishStateFirst,
		func() workflow.Status {
			if err := r.updateOmDeploymentRs(conn, mrs, log); err != nil {
				return workflow.Failed(err.Error())
			}
			return workflow.OK()
		},
		func() workflow.Status {
			return r.reconcileMemberResources(mrs, log, conn, projectConfig)
		})

	if !status.IsOK() {
		return r.updateStatus(&mrs, status, log)
	}

	if err := r.saveLastAchievedSpec(mrs); err != nil {
		return r.updateStatus(&mrs, workflow.Failed("Failed to set annotation: %s", err), log)
	}

	// for purposes of comparison, we don't want to compare entries with 0 members since they will not be present
	// as a desired entry.
	desiredSpecList := mrs.GetDesiredSpecList()
	actualSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return r.updateStatus(&mrs, workflow.Failed(err.Error()), log)
	}

	effectiveSpecList := filterClusterSpecItem(actualSpecList, func(item mdbmultiv1.ClusterSpecItem) bool {
		return item.Members > 0
	})

	// sort both actual and desired to match the effective and desired list version before comparing
	sortClusterSpecList(desiredSpecList)
	sortClusterSpecList(effectiveSpecList)

	needToRequeue := !reflect.DeepEqual(desiredSpecList, effectiveSpecList)
	if needToRequeue {
		return r.updateStatus(&mrs, workflow.Pending("MongoDBMulti deployment is not yet ready, requeing reconciliation."), log)
	}

	log.Infow("Finished reconciliation for MultiReplicaSet", "Spec", mrs.Spec, "Status", mrs.Status)
	return r.updateStatus(&mrs, workflow.OK(), log)
}

// needToPublishStateFirstMultiCluster returns a boolean indicating whether or not Ops Manager
// needs to be updated before the StatefulSets are created for this resource.
func (r *ReconcileMongoDbMultiReplicaSet) needToPublishStateFirstMultiCluster(mrs *mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) (bool, error) {
	scalingDown, err := isScalingDown(mrs)
	if err != nil {
		return false, fmt.Errorf("failed determining if the resource is scaling down: %s", err)
	}

	if scalingDown {
		log.Infof("Scaling down in progress, updating Ops Manager state first.")
		return true, nil
	}

	firstStatefulSet, err := r.firstStatefulSet(mrs)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// No need to publish state as this is a new StatefulSet
			log.Debugf("New StatefulSet %s", firstStatefulSet.GetName())
			return false, nil
		}
		return false, err
	}

	databaseContainer := container.GetByName(util.DatabaseContainerName, firstStatefulSet.Spec.Template.Spec.Containers)
	volumeMounts := databaseContainer.VolumeMounts
	if mrs.Spec.Security != nil {
		if !mrs.Spec.Security.IsTLSEnabled() && statefulset.VolumeMountWithNameExists(volumeMounts, util.SecretVolumeName) {
			log.Debug("About to set `security.tls.enabled` to false. automationConfig needs to be updated first")
			return true, nil
		}

		if mrs.Spec.Security.TLSConfig.CA == "" && statefulset.VolumeMountWithNameExists(volumeMounts, tls.ConfigMapVolumeCAName) {
			log.Debug("About to set `security.tls.CA` to empty. automationConfig needs to be updated first")
			return true, nil
		}
	}

	return false, nil
}

// isScalingDown returns true if the MongoDBMulti is attempting to scale down.
func isScalingDown(mrs *mdbmultiv1.MongoDBMulti) (bool, error) {
	desiredSpec := mrs.Spec.GetClusterSpecList()

	specThisReconciliation, err := mrs.GetClusterSpecItems()
	if err != nil {
		return false, err
	}

	if len(desiredSpec) < len(specThisReconciliation) {
		return true, nil
	}

	for i := 0; i < len(specThisReconciliation); i++ {
		specItem := desiredSpec[i]
		reconciliationItem := specThisReconciliation[i]

		if specItem.Members < reconciliationItem.Members {
			// when failover is happening, the clusterspec list will alaways have fewer members
			// than the specs for the reoconcile.
			if _, ok := mdbmultiv1.HasClustersToFailOver(mrs.Annotations); ok {
				return false, nil
			}
			return true, nil
		}

	}

	return false, nil
}

func (r *ReconcileMongoDbMultiReplicaSet) firstStatefulSet(mrs *mdbmultiv1.MongoDBMulti) (appsv1.StatefulSet, error) {
	// We want to get an existing statefulset, so we should fetch the client from "mrs.Spec.ClusterSpecList.ClusterSpecs"
	// instead of mrs.GetClusterSpecItems(), since the later returns the effective clusterspecs, which might return
	// clusters which have been removed and do not have a running statefulset.
	items := mrs.Spec.ClusterSpecList.ClusterSpecs
	var firstMemberClient kubernetesClient.Client
	var firstMemberIdx int
	for idx, item := range items {
		client, ok := r.memberClusterClientsMap[item.ClusterName]
		if ok {
			firstMemberClient = client
			firstMemberIdx = idx
			break
		}
	}
	stsName := kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(items[firstMemberIdx].ClusterName)))

	firstStatefulSet, err := firstMemberClient.GetStatefulSet(stsName)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return firstStatefulSet, err
		}
		return firstStatefulSet, fmt.Errorf("error getting StatefulSet %s: %s", stsName, err)
	}
	return firstStatefulSet, err
}

// reconcileMemberResources handles the synchronization of kubernetes resources, which can be statefulsets, services etc.
// All the resources required in the k8s cluster (as opposed to the automation config) for creating the replicaset
// should be reconciled in this method.
func (r *ReconcileMongoDbMultiReplicaSet) reconcileMemberResources(mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger, conn om.Connection, projectConfig mdbv1.ProjectConfig) workflow.Status {
	err := r.reconcileServices(log, &mrs)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	// create configmap with the hostnameoverride
	err = r.reconcileHostnameOverrideConfigMap(log, mrs)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	// Copy over OM CustomCA if specified in project config
	if projectConfig.SSLMMSCAConfigMap != "" {
		err = r.reconcileOMCAConfigMap(log, mrs, projectConfig.SSLMMSCAConfigMap)
		if err != nil {
			return workflow.Failed(err.Error())
		}
	}

	return r.reconcileStatefulSets(mrs, log, conn, projectConfig)
}

func (r *ReconcileMongoDbMultiReplicaSet) reconcileStatefulSets(mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger, conn om.Connection, projectConfig mdbv1.ProjectConfig) workflow.Status {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return workflow.Failed(fmt.Sprintf("failed to read cluster spec list: %s", err))
	}
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		log.Errorf("failed retrieving list of failed clusters: %s", err.Error())
	}

	for _, item := range clusterSpecList {
		if stringutil.Contains(failedClusterNames, item.ClusterName) {
			log.Warnf(fmt.Sprintf("failed to reconcile statefulset: cluster %s is marked as failed", item.ClusterName))
			continue
		}

		memberClient, ok := r.memberClusterClientsMap[item.ClusterName]
		if !ok {
			log.Warnf(fmt.Sprintf("failed to reconcile statefulset: cluster %s missing from client map", item.ClusterName))
			continue
		}
		secretMemberClient := r.memberClusterSecretClientsMap[item.ClusterName]
		replicasThisReconciliation, err := getMembersForClusterSpecItemThisReconciliation(&mrs, item)
		clusterNum := mrs.ClusterNum(item.ClusterName)
		if err != nil {
			return workflow.Failed(err.Error())
		}

		// Copy over the CA config map if it exists on the central cluster
		caConfigMapName := mrs.Spec.Security.TLSConfig.CA
		if caConfigMapName != "" {
			cm, err := r.client.GetConfigMap(kube.ObjectKey(mrs.Namespace, caConfigMapName))
			if err != nil {
				return workflow.Failed(fmt.Sprintf("Expected CA ConfigMap not found on central cluster: %s", caConfigMapName))
			}

			memberCm := configmap.Builder().SetName(caConfigMapName).SetNamespace(mrs.Namespace).SetData(cm.Data).Build()
			err = configmap.CreateOrUpdate(memberClient, memberCm)

			if err != nil && !apiErrors.IsAlreadyExists(err) {
				return workflow.Failed(fmt.Sprintf("Failed to sync CA ConfigMap in cluster: %s, err: %s", item.ClusterName, err))
			}
		}

		// Ensure TLS for multi-cluster statefulset in each cluster
		mrsConfig := certs.MultiReplicaSetConfig(mrs, clusterNum, replicasThisReconciliation)
		if status := certs.EnsureSSLCertsForStatefulSet(r.SecretClient, secretMemberClient, *mrs.Spec.Security, mrsConfig, log); !status.IsOK() {
			return status
		}

		currentAgentAuthMode, err := conn.GetAgentAuthMode()
		if err != nil {
			return workflow.Failed(fmt.Sprintf("Failed to retrieve current agent auth mode in cluster: %s, err: %s", item.ClusterName, err))
		}
		certConfigurator := certs.MongoDBMultiX509CertConfigurator{
			MongoDBMulti:      &mrs,
			ClusterNum:        clusterNum,
			Replicas:          replicasThisReconciliation,
			SecretReadClient:  r.SecretClient,
			SecretWriteClient: secretMemberClient,
		}
		if status := r.ensureX509SecretAndCheckTLSType(certConfigurator, currentAgentAuthMode, log); !status.IsOK() {
			return status
		}

		// copy the agent api key to the member cluster.
		apiKeySecretName := fmt.Sprintf("%s-group-secret", conn.GroupID())
		secretByte, err := secret.ReadByteData(r.client, types.NamespacedName{Name: apiKeySecretName, Namespace: mrs.Namespace})
		if err != nil {
			return workflow.Failed(err.Error())
		}

		secretObject := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apiKeySecretName,
				Namespace: mrs.Namespace,
				Labels:    mongoDBMultiLabels(mrs.Name, mrs.Namespace),
			},
			Data: secretByte,
		}

		err = secret.CreateOrUpdate(memberClient, secretObject)
		if err != nil {
			return workflow.Failed(err.Error())
		}

		errorStringFormatStr := "failed to create StatefulSet in cluster: %s, err: %s"

		// get cert hash of tls secret if it exists
		certHash := enterprisepem.ReadHashFromSecret(r.SecretClient, mrs.Namespace, mrsConfig.CertSecretName, "", log)

		log.Debugf("Creating StatefulSet %s with %d replicas in cluster: %s", mrs.MultiStatefulsetName(clusterNum), replicasThisReconciliation, item.ClusterName)

		sts, err := mconstruct.MultiClusterStatefulSet(mrs, clusterNum, replicasThisReconciliation, conn, projectConfig, item.StatefulSetConfiguration, certHash)
		if err != nil {
			return workflow.Failed(fmt.Sprintf(errorStringFormatStr, item.ClusterName, err))
		}

		deleteSts, err := shouldDeleteStatefulSet(mrs, item)
		if err != nil {
			return workflow.Failed(fmt.Sprintf(errorStringFormatStr, item.ClusterName, err))
		}

		if deleteSts {
			if err := memberClient.Delete(context.TODO(), &sts); err != nil && !apiErrors.IsNotFound(err) {
				return workflow.Failed(fmt.Sprintf("failed to delete StatefulSet in cluster: %s, err: %s", item.ClusterName, err))
			}
			continue
		}

		_, err = enterprisests.CreateOrUpdateStatefulset(memberClient, mrs.Namespace, log, &sts)
		if err != nil {
			return workflow.Failed(fmt.Sprintf("failed to create/update StatefulSet in cluster: %s, err: %s", item.ClusterName, err))
		}

		if status := getStatefulSetStatus(sts.Namespace, sts.Name, memberClient); !status.IsOK() {
			return status
		}

		log.Infof("Successfully ensured StatefulSet in cluster: %s", item.ClusterName)
	}
	return workflow.OK()
}

// shouldDeleteStatefulSet returns a boolean value indicating whether or not the StatefulSet associated with
// the given cluster spec item should be deleted or not.
func shouldDeleteStatefulSet(mrs mdbmultiv1.MongoDBMulti, item mdbmultiv1.ClusterSpecItem) (bool, error) {
	for _, specItem := range mrs.Spec.ClusterSpecList.ClusterSpecs {
		if item.ClusterName == specItem.ClusterName {
			// this spec value has been explicitly defined, don't delete it.
			return false, nil
		}
	}

	items, err := mrs.GetClusterSpecItems()
	if err != nil {
		return false, err
	}

	for _, specItem := range items {
		if item.ClusterName == specItem.ClusterName {
			// we delete only if we have fully scaled down and are at 0 members
			return specItem.Members == 0, nil
		}
	}

	// we are in the process of scaling down to 0, and should not yet delete the statefulset
	return false, nil
}

// getMembersForClusterSpecItemThisReconciliation returns the value members should have for a given cluster spec item
// for a given reconciliation. This value should increment or decrement in one cluster by one member each reconciliation
// when a scaling operation is taking place.
func getMembersForClusterSpecItemThisReconciliation(mrs *mdbmultiv1.MongoDBMulti, item mdbmultiv1.ClusterSpecItem) (int, error) {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return -1, err
	}
	for _, clusterItem := range clusterSpecList {
		if clusterItem.ClusterName == item.ClusterName {
			return clusterItem.Members, nil
		}
	}
	return -1, fmt.Errorf("did not find %s in cluster spec list", item.ClusterName)
}

// saveLastAchievedSpec updates the MongoDBMulti resource with the spec that was just achieved.
func (r *ReconcileMongoDbMultiReplicaSet) saveLastAchievedSpec(mrs mdbmultiv1.MongoDBMulti) error {
	clusterSpecs, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}

	lastAchievedSpec := mrs.Spec
	lastAchievedSpec.ClusterSpecList.ClusterSpecs = clusterSpecs
	achievedSpecBytes, err := json.Marshal(lastAchievedSpec)
	if err != nil {
		return err
	}

	if mrs.Annotations == nil {
		mrs.Annotations = map[string]string{}
	}

	// TODO Find a way to avoid using the spec for this field as we're not writing the information
	// back in the resource and the user does not set it.
	clusterNumBytes, err := json.Marshal(mrs.Spec.Mapping)
	if err != nil {
		return err
	}
	annotationsToAdd := make(map[string]string)

	annotationsToAdd[util.LastAchievedSpec] = string(achievedSpecBytes)
	if string(clusterNumBytes) != "null" {
		annotationsToAdd[mdbmultiv1.LastClusterNumMapping] = string(clusterNumBytes)
	}

	return annotations.SetAnnotations(mrs.DeepCopy(), annotationsToAdd, r.client)
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (r *ReconcileMongoDbMultiReplicaSet) updateOmDeploymentRs(conn om.Connection, mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {
	hostnames := make([]string, 0)

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}

	for _, spec := range clusterSpecList {
		hostnamesToAdd := dns.GetMultiClusterAgentHostnames(mrs.Name, mrs.Namespace, mrs.ClusterNum(spec.ClusterName), spec.Members)
		hostnames = append(hostnames, hostnamesToAdd...)
	}

	err = agents.WaitForRsAgentsToRegisterReplicasSpecifiedMultiCluster(conn, hostnames, log)
	if err != nil {
		return err
	}

	processIds, err := getExistingProcessIds(conn, mrs)
	if err != nil {
		return err
	}
	log.Debugf("Existing process Ids: %+v", processIds)

	certificateFileName := ""

	// If tls is enabled we need to configure the "processes" array in opsManager/Cloud Manager with the
	// correct certFilePath, with the new tls design, this path has the certHash in it(so that cert can be rotated
	//	without pod restart), we can get the cert hash from any of the statefulset, here we pick the statefulset in the first cluster.
	if mrs.Spec.Security.IsTLSEnabled() {
		firstStatefulSet, err := r.firstStatefulSet(&mrs)

		if err != nil {
			return err
		}
		if certificateHash, ok := firstStatefulSet.Annotations["certHash"]; ok {
			certificateFileName = fmt.Sprintf("%s/%s", util.TLSCertMountPath, certificateHash)
		}
	}

	processes, err := process.CreateMongodProcessesWithLimitMulti(mrs, certificateFileName)
	if err != nil {
		return err
	}

	rs := om.NewMultiClusterReplicaSetWithProcesses(om.NewReplicaSet(mrs.Name, mrs.Spec.Version), processes, processIds, mrs.Spec.Connectivity)

	caFilePath := fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)

	status, additionalReconciliationRequired := r.updateOmAuthentication(conn, rs.GetProcessNames(), &mrs, "", caFilePath, log)
	if !status.IsOK() {
		return fmt.Errorf("failed to enable Authentication for MongoDB Multi Replicaset")
	}

	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			d.MergeReplicaSet(rs, mrs.Spec.AdditionalMongodConfig.ToMap(), mrs.GetLastAdditionalMongodConfig(), log)
			d.AddMonitoringAndBackup(log, mrs.Spec.GetSecurity().IsTLSEnabled(), caFilePath)
			d.ConfigureTLS(mrs.Spec.GetSecurity(), caFilePath)
			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	if additionalReconciliationRequired {
		// TODO: fix this decide when to use Pending vs Reconciling
		return fmt.Errorf("failed to complete reconciliation")
	}

	status = r.ensureBackupConfigurationAndUpdateStatus(conn, &mrs, r.SecretClient, log)
	if !status.IsOK() {
		return fmt.Errorf("failed to configure backup for MongoDBMulti RS")
	}

	if err := om.WaitForReadyState(conn, rs.GetProcessNames(), log); err != nil {
		return err
	}
	return nil
}

func getExistingProcessIds(conn om.Connection, mrs mdbmultiv1.MongoDBMulti) (map[string]int, error) {
	existingDeployment, err := conn.ReadDeployment()
	if err != nil {
		return nil, err
	}

	processIds := map[string]int{}
	for _, rs := range existingDeployment.ReplicaSetsCopy() {
		if rs.Name() != mrs.Name {
			continue
		}
		for _, m := range rs.Members() {
			processIds[m.Name()] = m.Id()
		}
	}
	return processIds, nil
}

func mongoDBMultiLabels(name, namespace string) map[string]string {
	return map[string]string{
		"controller":   "mongodb-enterprise-operator",
		"mongodbmulti": fmt.Sprintf("%s-%s", namespace, name),
	}
}

func getSRVService(mrs *mdbmultiv1.MongoDBMulti) corev1.Service {
	svcLabels := mongoDBMultiLabels(mrs.Name, mrs.Namespace)

	svc := service.Builder().
		SetName(fmt.Sprintf("%s-svc", mrs.Name)).
		SetNamespace(mrs.Namespace).
		SetSelector(mconstruct.PodLabel(mrs.Name)).
		SetLabels(svcLabels).
		SetPublishNotReadyAddresses(true).
		AddPort(&corev1.ServicePort{Port: 27017, Name: "mongodb"}).
		AddPort(&corev1.ServicePort{Port: 27018, Name: "backup", TargetPort: intstr.IntOrString{IntVal: 27018}}).
		Build()

	return svc
}

func getService(mrs *mdbmultiv1.MongoDBMulti, clusterName string, podNum int) corev1.Service {
	svcLabels := map[string]string{
		"statefulset.kubernetes.io/pod-name": dns.GetMultiPodName(mrs.Name, mrs.ClusterNum(clusterName), podNum),
		"controller":                         "mongodb-enterprise-operator",
		"mongodbmulti":                       fmt.Sprintf("%s-%s", mrs.Namespace, mrs.Name),
	}

	labelSelectors := map[string]string{
		"statefulset.kubernetes.io/pod-name": dns.GetMultiPodName(mrs.Name, mrs.ClusterNum(clusterName), podNum),
		"controller":                         "mongodb-enterprise-operator",
	}

	svc := service.Builder().
		SetName(dns.GetServiceName(mrs.Name, mrs.ClusterNum(clusterName), podNum)).
		SetNamespace(mrs.Namespace).
		SetSelector(labelSelectors).
		SetLabels(svcLabels).
		SetPublishNotReadyAddresses(true).
		AddPort(&corev1.ServicePort{Port: 27017, Name: "mongodb"}).
		AddPort(&corev1.ServicePort{Port: 27018, Name: "backup", TargetPort: intstr.IntOrString{IntVal: 27018}}).
		Build()

	return svc
}

// reconcileServices makes sure that we have a service object corresponding to each statefulset pod
// in the member clusters
func (r *ReconcileMongoDbMultiReplicaSet) reconcileServices(log *zap.SugaredLogger, mrs *mdbmultiv1.MongoDBMulti) error {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		log.Errorf("failed retrieving list of failed clusters: %s", err.Error())
	}

	// by default we would create the duplicate services
	shouldCreateDuplicates := mrs.Spec.DuplicateServiceObjects == nil || *mrs.Spec.DuplicateServiceObjects
	if shouldCreateDuplicates {
		// iterate over each cluster and create service object corresponding to each of the pods in the multi-cluster RS.
		for k, v := range r.memberClusterClientsMap {
			for _, e := range clusterSpecList {
				if stringutil.Contains(failedClusterNames, e.ClusterName) {
					log.Warnf("failed to create duplicate service: cluster %s is marked as failed", e.ClusterName)
					continue
				}
				for podNum := 0; podNum < e.Members; podNum++ {
					svc := getService(mrs, e.ClusterName, podNum)
					err := service.CreateOrUpdateService(v, svc)

					if err != nil && !apiErrors.IsAlreadyExists(err) {
						return fmt.Errorf("failed to created service: %s in cluster: %s, err: %v", svc.Name, k, err)
					}
					log.Infof("Successfully created service: %s in cluster: %s", svc.Name, k)
				}
			}
		}
		return nil
	}

	for _, e := range clusterSpecList {
		if stringutil.Contains(failedClusterNames, e.ClusterName) {
			log.Warnf(fmt.Sprintf("failed to create service: cluster %s is marked as failed", e.ClusterName))
			continue
		}

		client, ok := r.memberClusterClientsMap[e.ClusterName]
		if !ok {
			log.Warnf(fmt.Sprintf("failed to create service: cluster %s missing from client map", e.ClusterName))
			continue
		}
		if e.Members == 0 {
			log.Warnf("skipping service creation: no members assigned to cluster %s", e.ClusterName)
			continue
		}

		svc := getSRVService(mrs)
		err = service.CreateOrUpdateService(client, svc)
		if err != nil && !apiErrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create service: %s in cluster: %s, err :%v", svc.Name, e.ClusterName, err)
		}
		log.Infof("Successfully created service: %s in cluster: %s", svc.Name, e.ClusterName)

		for podNum := 0; podNum < e.Members; podNum++ {
			svc := getService(mrs, e.ClusterName, podNum)
			err := service.CreateOrUpdateService(client, svc)
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to created service: %s in cluster: %s, err: %v", svc.Name, e.ClusterName, err)
			}

			log.Infof("Successfully created service: %s in cluster: %s", svc.Name, e.ClusterName)
		}
	}
	return nil
}

func getHostnameOverrideConfigMap(mrs mdbmultiv1.MongoDBMulti, clusterNum int, members int) corev1.ConfigMap {
	data := make(map[string]string)

	for podNum := 0; podNum < members; podNum++ {
		key := dns.GetMultiPodName(mrs.Name, clusterNum, podNum)
		value := fmt.Sprintf("%s.%s.svc.cluster.local", dns.GetServiceName(mrs.Name, clusterNum, podNum), mrs.Namespace)
		data[key] = value
	}

	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-hostname-override", mrs.Name),
			Namespace: mrs.Namespace,
			Labels:    mongoDBMultiLabels(mrs.Name, mrs.Namespace),
		},
		Data: data,
	}
	return cm
}

func (r *ReconcileMongoDbMultiReplicaSet) reconcileHostnameOverrideConfigMap(log *zap.SugaredLogger, mrs mdbmultiv1.MongoDBMulti) error {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		log.Warnf("failed retrieving list of failed clusters: %s", err.Error())
	}

	for i, e := range clusterSpecList {
		if stringutil.Contains(failedClusterNames, e.ClusterName) {
			log.Warnf(fmt.Sprintf("failed to create configmap: cluster %s is marked as failed", e.ClusterName))
			continue
		}

		client, ok := r.memberClusterClientsMap[e.ClusterName]
		if !ok {
			log.Warnf(fmt.Sprintf("failed to create configmap: cluster %s is missing from client map", e.ClusterName))
			continue
		}
		cm := getHostnameOverrideConfigMap(mrs, i, e.Members)

		err = configmap.CreateOrUpdate(client, cm)
		if err != nil && !apiErrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create configmap: %s in cluster: %s, err: %v", cm.Name, e.ClusterName, err)
		}
		log.Infof("Successfully ensured configmap: %s in cluster: %s", cm.Name, e.ClusterName)

	}
	return nil
}

func (r *ReconcileMongoDbMultiReplicaSet) reconcileOMCAConfigMap(log *zap.SugaredLogger, mrs mdbmultiv1.MongoDBMulti, configMapName string) error {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		log.Warnf("failed retrieving list of failed clusters: %s", err.Error())
	}

	cm, err := r.client.GetConfigMap(kube.ObjectKey(mrs.Namespace, configMapName))
	if err != nil {
		return err
	}
	for _, cluster := range clusterSpecList {
		if stringutil.Contains(failedClusterNames, cluster.ClusterName) {
			log.Warnf("failed to create configmap %s: cluster %s is marked as failed", configMapName, cluster.ClusterName)
			continue
		}
		client := r.memberClusterClientsMap[cluster.ClusterName]
		memberCm := configmap.Builder().SetName(configMapName).SetNamespace(mrs.Namespace).SetData(cm.Data).Build()
		err := configmap.CreateOrUpdate(client, memberCm)
		if err != nil && !apiErrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create configmap: %s in cluster %s, err: %v", cm.Name, cluster.ClusterName, err)
		}
		log.Infof("Sucessfully ensured configmap: %s in cluster: %s", cm.Name, cluster.ClusterName)
	}
	return nil
}

// AddMultiReplicaSetController creates a new MongoDbMultiReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddMultiReplicaSetController(mgr manager.Manager, memberClustersMap map[string]cluster.Cluster) error {
	reconciler := newMultiClusterReplicaSetReconciler(mgr, om.NewOpsManagerConnection, memberClustersMap)
	c, err := controller.New(util.MongoDbMultiController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	eventHandler := ResourceEventHandler{deleter: reconciler}
	err = c.Watch(&source.Kind{Type: &mdbmultiv1.MongoDBMulti{}}, &eventHandler, predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mdbmultiv1.MongoDBMulti)
			newResource := e.ObjectNew.(*mdbmultiv1.MongoDBMulti)

			oldSpecAnnotation := oldResource.GetAnnotations()[util.LastAchievedSpec]
			newSpecAnnotation := newResource.GetAnnotations()[util.LastAchievedSpec]

			// don't handle an update to just the previous spec annotation if they are not the same.
			// this prevents the operator triggering reconciliations on resource that it is updating itself.
			if !reflect.DeepEqual(oldSpecAnnotation, newSpecAnnotation) {
				return false
			}

			return reflect.DeepEqual(oldResource.GetStatus(), newResource.GetStatus())
		},
	})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.Secret, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	// register watcher across member clusters
	for k, v := range memberClustersMap {
		err := c.Watch(source.NewKindWithCache(&appsv1.StatefulSet{}, v.GetCache()), &khandler.EnqueueRequestForOwnerMultiCluster{}, watch.PredicatesForMultiStatefulSet())
		if err != nil {
			return fmt.Errorf("failed to set Watch on member cluster: %s, err: %v", k, err)
		}
	}

	// the operator watches the member clusters' API servers to determine whether the clsters are healthy or not
	eventChannel := make(chan event.GenericEvent)
	memberClusterMap := memberwatch.MemberClusterMap{Cache: make(map[string]*memberwatch.MemberHeathCheck)}
	go memberClusterMap.WatchMemberClusterHealth(zap.S(), eventChannel, reconciler.memberClusterClientsMap, reconciler.client)

	err = c.Watch(
		&source.Channel{Source: eventChannel},
		&handler.EnqueueRequestForObject{},
	)
	if err != nil {
		zap.S().Errorf("failed to watch for member cluster healthcheck: %w", err)
	}

	zap.S().Infof("Registered controller %s", util.MongoDbMultiReplicaSetController)
	return err
}

// OnDelete cleans up Ops Manager state and all Kubernetes resources associated with this instance.
func (r *ReconcileMongoDbMultiReplicaSet) OnDelete(obj runtime.Object, log *zap.SugaredLogger) error {
	mrs := obj.(*mdbmultiv1.MongoDBMulti)
	return r.deleteManagedResources(*mrs, log)
}

// cleanOpsManagerState removes the project configuration (processes, auth settings etc.) from the corresponding OM project.
func (r *ReconcileMongoDbMultiReplicaSet) cleanOpsManagerState(mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, &mrs, log)
	if err != nil {
		return err
	}

	log.Infow("Removing replica set from Ops Manager", "config", mrs.Spec)
	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, mrs.Namespace, log)
	if err != nil {
		return err
	}

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			processNames = d.GetProcessNames(om.ReplicaSet{}, mrs.Name)
			// error means that replica set is not in the deployment - it's ok and we can proceed (could happen if
			// deletion cleanup happened twice and the first one cleaned OM state already)
			if e := d.RemoveReplicaSetByName(mrs.Name, log); e != nil {
				log.Warnf("Failed to remove replica set from automation config: %s", e)
			}

			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	hostsToRemove, err := mrs.GetMultiClusterAgentHostnames()
	if err != nil {
		return err
	}
	log.Infow("Stop monitoring removed hosts in Ops Manager", "removedHosts", hostsToRemove)

	if err = host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	opts := authentication.Options{
		AuthoritativeSet: false,
		ProcessNames:     processNames,
	}

	if err := authentication.Disable(conn, opts, true, log); err != nil {
		return err
	}
	log.Infof("Removed deployment %s from Ops Manager at %s", mrs.Name, conn.BaseURL())
	return nil
}

// deleteManagedResources deletes resources across all member clusters that are owned by this MongoDBMulti resource.
func (r *ReconcileMongoDbMultiReplicaSet) deleteManagedResources(mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {
	var errs error
	if err := r.cleanOpsManagerState(mrs, log); err != nil {
		errs = multierror.Append(errs, err)
	}

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}

	for _, item := range clusterSpecList {
		c := r.memberClusterClientsMap[item.ClusterName]
		if err := r.deleteClusterResources(c, mrs, log); err != nil {
			errs = multierror.Append(errs, fmt.Errorf("failed deleting dependant resources in cluster %s: %s", item.ClusterName, err))
		}
	}
	return errs
}

// deleteClusterResources removes all resources that are associated with the given MongoDBMulti resource in a given cluster.
func (r *ReconcileMongoDbMultiReplicaSet) deleteClusterResources(c kubernetesClient.Client, mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {
	var errs error

	// cleanup resources in the namespace as the MongoDBMulti with the corresponding label.
	cleanupOptions := mongodbCleanUpOptions{
		namespace: mrs.Namespace,
		labels:    mongoDBMultiLabels(mrs.Name, mrs.Namespace),
	}

	if err := c.DeleteAllOf(context.TODO(), &corev1.Service{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed Serivces associated with %s/%s", mrs.Namespace, mrs.Name)
	}

	if err := c.DeleteAllOf(context.TODO(), &appsv1.StatefulSet{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed StatefulSets associated with %s/%s", mrs.Namespace, mrs.Name)
	}

	if err := c.DeleteAllOf(context.TODO(), &corev1.ConfigMap{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed ConfigMaps associated with %s/%s", mrs.Namespace, mrs.Name)
	}

	if err := c.DeleteAllOf(context.TODO(), &corev1.Secret{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed Secrets associated with %s/%s", mrs.Namespace, mrs.Name)
	}

	r.RemoveDependentWatchedResources(kube.ObjectKey(mrs.Namespace, mrs.Name))

	return errs
}

// filterClusterSpecItem filters items out of a list based on provided predicate.
func filterClusterSpecItem(items []mdbmultiv1.ClusterSpecItem, fn func(item mdbmultiv1.ClusterSpecItem) bool) []mdbmultiv1.ClusterSpecItem {
	var result []mdbmultiv1.ClusterSpecItem
	for _, item := range items {
		if fn(item) {
			result = append(result, item)
		}
	}
	return result
}

func sortClusterSpecList(clusterSpecList []mdbmultiv1.ClusterSpecItem) {
	sort.SliceStable(clusterSpecList, func(i, j int) bool {
		return clusterSpecList[i].ClusterName < clusterSpecList[j].ClusterName
	})
}
