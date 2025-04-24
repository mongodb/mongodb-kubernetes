package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/process"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	mconstruct "github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/multicluster"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/recovery"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	mcoConstruct "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/controllers/construct"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/annotations"
	kubernetesClient "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/service"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	khandler "github.com/10gen/ops-manager-kubernetes/pkg/handler"
	"github.com/10gen/ops-manager-kubernetes/pkg/images"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	mekoService "github.com/10gen/ops-manager-kubernetes/pkg/kube/service"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster/memberwatch"
	"github.com/10gen/ops-manager-kubernetes/pkg/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
)

// ReconcileMongoDbMultiReplicaSet reconciles a MongoDB ReplicaSet across multiple Kubernetes clusters
type ReconcileMongoDbMultiReplicaSet struct {
	*ReconcileCommonController
	omConnectionFactory           om.ConnectionFactory
	memberClusterClientsMap       map[string]kubernetesClient.Client // holds the client for each of the memberclusters(where the MongoDB ReplicaSet is deployed)
	memberClusterSecretClientsMap map[string]secrets.SecretClient
	forceEnterprise               bool

	imageUrls                         images.ImageUrls
	initDatabaseNonStaticImageVersion string
	databaseNonStaticImageVersion     string
}

var _ reconcile.Reconciler = &ReconcileMongoDbMultiReplicaSet{}

func newMultiClusterReplicaSetReconciler(ctx context.Context, kubeClient client.Client, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise bool, omFunc om.ConnectionFactory, memberClustersMap map[string]client.Client) *ReconcileMongoDbMultiReplicaSet {
	clientsMap := make(map[string]kubernetesClient.Client)
	secretClientsMap := make(map[string]secrets.SecretClient)

	// extract client from each cluster object.
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
		secretClientsMap[k] = secrets.SecretClient{
			VaultClient: nil, // Vault is not supported yet on multicluster
			KubeClient:  clientsMap[k],
		}
	}

	return &ReconcileMongoDbMultiReplicaSet{
		ReconcileCommonController:         NewReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:               omFunc,
		memberClusterClientsMap:           clientsMap,
		memberClusterSecretClientsMap:     secretClientsMap,
		forceEnterprise:                   forceEnterprise,
		imageUrls:                         imageUrls,
		initDatabaseNonStaticImageVersion: initDatabaseNonStaticImageVersion,
		databaseNonStaticImageVersion:     databaseNonStaticImageVersion,
	}
}

// MongoDBMultiCluster Resource
// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbmulticluster,mongodbmulticluster/status,mongodbmulticluster/finalizers},verbs=*,namespace=placeholder

// Reconcile reads that state of the cluster for a MongoDbMultiReplicaSet object and makes changes based on the state read
// and what is in the MongoDbMultiReplicaSet.Spec
func (r *ReconcileMongoDbMultiReplicaSet) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MultiReplicaSet", request.NamespacedName)
	log.Info("-> MultiReplicaSet.Reconcile")

	// Fetch the MongoDBMultiCluster instance
	mrs := mdbmultiv1.MongoDBMultiCluster{}
	if reconcileResult, err := r.prepareResourceForReconciliation(ctx, request, &mrs, log); err != nil {
		if apiErrors.IsNotFound(err) {
			return workflow.Invalid("Object for reconciliation not found").ReconcileResult()
		}
		log.Errorf("error preparing resource for reconciliation: %s", err)
		return reconcileResult, err
	}

	if !architectures.IsRunningStaticArchitecture(mrs.Annotations) {
		agents.UpgradeAllIfNeeded(ctx, agents.ClientSecret{Client: r.client, SecretClient: r.SecretClient}, r.omConnectionFactory, GetWatchedNamespace(), true)
	}

	if err := mrs.ProcessValidationsOnReconcile(nil); err != nil {
		return r.updateStatus(ctx, &mrs, workflow.Invalid("%s", err.Error()), log)
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.client, r.SecretClient, &mrs, log)
	if err != nil {
		return r.updateStatus(ctx, &mrs, workflow.Failed(xerrors.Errorf("Error reading project config and credentials: %w", err)), log)
	}

	conn, _, err := connection.PrepareOpsManagerConnection(ctx, r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, mrs.Namespace, log)
	if err != nil {
		return r.updateStatus(ctx, &mrs, workflow.Failed(xerrors.Errorf("error establishing connection to Ops Manager: %w", err)), log)
	}

	log = log.With("MemberCluster Namespace", mrs.Namespace)

	// check if resource has failedCluster annotation and mark it as failed if automated failover is not enabled
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		return r.updateStatus(ctx, &mrs, workflow.Failed(err), log)
	}
	if len(failedClusterNames) > 0 && !multicluster.ShouldPerformFailover() {
		return r.updateStatus(ctx, &mrs, workflow.Failed(xerrors.Errorf("resource has failed clusters in the annotation: %+v", failedClusterNames)), log)
	}

	r.SetupCommonWatchers(&mrs, nil, nil, mrs.Name)

	publishAutomationConfigFirst, err := r.publishAutomationConfigFirstMultiCluster(ctx, &mrs, log)
	if err != nil {
		return r.updateStatus(ctx, &mrs, workflow.Failed(err), log)
	}

	// Recovery prevents some deadlocks that can occur during reconciliation, e.g. the setting of an incorrect automation
	// configuration and a subsequent attempt to overwrite it later, the operator would be stuck in Pending phase.
	// See CLOUDP-189433 and CLOUDP-229222 for more details.
	if recovery.ShouldTriggerRecovery(mrs.Status.Phase != mdbstatus.PhaseRunning, mrs.Status.LastTransition) {
		log.Warnf("Triggering Automatic Recovery. The MongoDB resource %s/%s is in %s state since %s", mrs.Namespace, mrs.Name, mrs.Status.Phase, mrs.Status.LastTransition)
		automationConfigError := r.updateOmDeploymentRs(ctx, conn, mrs, true, log)
		reconcileStatus := r.reconcileMemberResources(ctx, &mrs, log, conn, projectConfig)
		if !reconcileStatus.IsOK() {
			log.Errorf("Recovery failed because of reconcile errors, %v", reconcileStatus)
		}
		if automationConfigError != nil {
			log.Errorf("Recovery failed because of Automation Config update errors, %w", automationConfigError)
		}
	}

	status := workflow.RunInGivenOrder(publishAutomationConfigFirst,
		func() workflow.Status {
			if err := r.updateOmDeploymentRs(ctx, conn, mrs, false, log); err != nil {
				return workflow.Failed(err)
			}
			return workflow.OK()
		},
		func() workflow.Status {
			return r.reconcileMemberResources(ctx, &mrs, log, conn, projectConfig)
		})

	if !status.IsOK() {
		return r.updateStatus(ctx, &mrs, status, log)
	}

	mrs.Status.FeatureCompatibilityVersion = mrs.CalculateFeatureCompatibilityVersion()
	if err := r.saveLastAchievedSpec(ctx, mrs); err != nil {
		return r.updateStatus(ctx, &mrs, workflow.Failed(xerrors.Errorf("Failed to set annotation: %w", err)), log)
	}

	// for purposes of comparison, we don't want to compare entries with 0 members since they will not be present
	// as a desired entry.
	desiredSpecList := mrs.GetDesiredSpecList()
	actualSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return r.updateStatus(ctx, &mrs, workflow.Failed(err), log)
	}

	effectiveSpecList := filterClusterSpecItem(actualSpecList, func(item mdb.ClusterSpecItem) bool {
		return item.Members > 0
	})

	// sort both actual and desired to match the effective and desired list version before comparing
	sortClusterSpecList(desiredSpecList)
	sortClusterSpecList(effectiveSpecList)

	needToRequeue := !clusterSpecListsEqual(effectiveSpecList, desiredSpecList)
	if needToRequeue {
		return r.updateStatus(ctx, &mrs, workflow.Pending("MongoDBMultiCluster deployment is not yet ready, requeuing reconciliation."), log)
	}

	log.Infow("Finished reconciliation for MultiReplicaSet", "Spec", mrs.Spec, "Status", mrs.Status)
	return r.updateStatus(ctx, &mrs, workflow.OK(), log, mdbstatus.NewPVCsStatusOptionEmptyStatus())
}

// publishAutomationConfigFirstMultiCluster returns a boolean indicating whether Ops Manager
// needs to be updated before the StatefulSets are created for this resource.
func (r *ReconcileMongoDbMultiReplicaSet) publishAutomationConfigFirstMultiCluster(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, log *zap.SugaredLogger) (bool, error) {
	if architectures.IsRunningStaticArchitecture(mrs.Annotations) {
		if mrs.IsInChangeVersion() {
			return true, nil
		}
	}

	scalingDown, err := isScalingDown(mrs)
	if err != nil {
		return false, xerrors.Errorf("failed determining if the resource is scaling down: %w", err)
	}

	if scalingDown {
		log.Infof("Scaling down in progress, updating Ops Manager state first.")
		return true, nil
	}

	firstStatefulSet, err := r.firstStatefulSet(ctx, mrs)
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

// isScalingDown returns true if the MongoDBMultiCluster is attempting to scale down.
func isScalingDown(mrs *mdbmultiv1.MongoDBMultiCluster) (bool, error) {
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
			// when failover is happening, the clusterspec list will always have fewer members
			// than the specs for the reconcile.
			if _, ok := mdbmultiv1.HasClustersToFailOver(mrs.Annotations); ok {
				return false, nil
			}
			return true, nil
		}

	}

	return false, nil
}

func (r *ReconcileMongoDbMultiReplicaSet) firstStatefulSet(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster) (appsv1.StatefulSet, error) {
	// We want to get an existing statefulset, so we should fetch the client from "mrs.Spec.ClusterSpecList.ClusterSpecs"
	// instead of mrs.GetClusterSpecItems(), since the later returns the effective clusterspecs, which might return
	// clusters which have been removed and do not have a running statefulset.
	items := mrs.Spec.ClusterSpecList
	var firstMemberClient kubernetesClient.Client
	var firstMemberIdx int
	foundOne := false
	for idx, item := range items {
		client, ok := r.memberClusterClientsMap[item.ClusterName]
		if ok {
			firstMemberClient = client
			firstMemberIdx = idx
			foundOne = true
			break
		}
	}
	if !foundOne {
		return appsv1.StatefulSet{}, xerrors.Errorf("was not able to find given member clusters in client map")
	}
	stsName := kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(items[firstMemberIdx].ClusterName)))

	firstStatefulSet, err := firstMemberClient.GetStatefulSet(ctx, stsName)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return firstStatefulSet, err
		}
		return firstStatefulSet, xerrors.Errorf("error getting StatefulSet %s: %w", stsName, err)
	}
	return firstStatefulSet, err
}

// reconcileMemberResources handles the synchronization of kubernetes resources, which can be statefulsets, services etc.
// All the resources required in the k8s cluster (as opposed to the automation config) for creating the replicaset
// should be reconciled in this method.
func (r *ReconcileMongoDbMultiReplicaSet) reconcileMemberResources(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, log *zap.SugaredLogger, conn om.Connection, projectConfig mdb.ProjectConfig) workflow.Status {
	err := r.reconcileServices(ctx, log, mrs)
	if err != nil {
		return workflow.Failed(err)
	}

	// create configmap with the hostname-override
	err = r.reconcileHostnameOverrideConfigMap(ctx, log, *mrs)
	if err != nil {
		return workflow.Failed(err)
	}

	// Copy over OM CustomCA if specified in project config
	if projectConfig.SSLMMSCAConfigMap != "" {
		err = r.reconcileOMCAConfigMap(ctx, log, *mrs, projectConfig.SSLMMSCAConfigMap)
		if err != nil {
			return workflow.Failed(err)
		}
	}
	// Ensure custom roles are created in OM
	if status := ensureRoles(mrs.GetSecurity().Roles, conn, log); !status.IsOK() {
		return status
	}

	return r.reconcileStatefulSets(ctx, mrs, log, conn, projectConfig)
}

type stsIdentifier struct {
	namespace   string
	name        string
	client      kubernetesClient.Client
	clusterName string
}

func (r *ReconcileMongoDbMultiReplicaSet) reconcileStatefulSets(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, log *zap.SugaredLogger, conn om.Connection, projectConfig mdb.ProjectConfig) workflow.Status {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return workflow.Failed(xerrors.Errorf("failed to read cluster spec list: %w", err))
	}
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		log.Errorf("failed retrieving list of failed clusters: %s", err.Error())
	}

	var stsLocators []stsIdentifier

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
		replicasThisReconciliation, err := getMembersForClusterSpecItemThisReconciliation(mrs, item)
		clusterNum := mrs.ClusterNum(item.ClusterName)
		if err != nil {
			return workflow.Failed(err)
		}

		// Copy over the CA config map if it exists on the central cluster
		caConfigMapName := mrs.Spec.Security.TLSConfig.CA
		if caConfigMapName != "" {
			cm, err := r.client.GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, caConfigMapName))
			if err != nil {
				return workflow.Failed(xerrors.Errorf("Expected CA ConfigMap not found on central cluster: %s", caConfigMapName))
			}

			memberCm := configmap.Builder().SetName(caConfigMapName).SetNamespace(mrs.Namespace).SetData(cm.Data).Build()
			err = configmap.CreateOrUpdate(ctx, memberClient, memberCm)

			if err != nil && !apiErrors.IsAlreadyExists(err) {
				return workflow.Failed(xerrors.Errorf("Failed to sync CA ConfigMap in cluster: %s, err: %w", item.ClusterName, err))
			}
		}

		// Ensure TLS for multi-cluster statefulset in each cluster
		mrsConfig := certs.MultiReplicaSetConfig(*mrs, clusterNum, item.ClusterName, replicasThisReconciliation)
		if status := certs.EnsureSSLCertsForStatefulSet(ctx, r.SecretClient, secretMemberClient, *mrs.Spec.Security, mrsConfig, log); !status.IsOK() {
			return status
		}

		automationConfig, err := conn.ReadAutomationConfig()
		if err != nil {
			return workflow.Failed(xerrors.Errorf("Failed to retrieve current automation config in cluster: %s, err: %w", item.ClusterName, err))
		}

		currentAgentAuthMode := automationConfig.GetAgentAuthMode()

		certConfigurator := certs.MongoDBMultiX509CertConfigurator{
			MongoDBMultiCluster: mrs,
			ClusterNum:          clusterNum,
			Replicas:            replicasThisReconciliation,
			SecretReadClient:    r.SecretClient,
			SecretWriteClient:   secretMemberClient,
		}
		if status := r.ensureX509SecretAndCheckTLSType(ctx, certConfigurator, currentAgentAuthMode, log); !status.IsOK() {
			return status
		}

		// copy the agent api key to the member cluster.
		apiKeySecretName := agents.ApiKeySecretName(conn.GroupID())
		secretByte, err := secret.ReadByteData(ctx, r.client, types.NamespacedName{Name: apiKeySecretName, Namespace: mrs.Namespace})
		if err != nil {
			return workflow.Failed(err)
		}

		secretObject := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apiKeySecretName,
				Namespace: mrs.Namespace,
				Labels:    mrs.GetOwnerLabels(),
			},
			Data: secretByte,
		}

		err = secret.CreateOrUpdate(ctx, memberClient, secretObject)
		if err != nil {
			return workflow.Failed(err)
		}

		// get cert hash of tls secret if it exists
		certHash := enterprisepem.ReadHashFromSecret(ctx, r.SecretClient, mrs.Namespace, mrsConfig.CertSecretName, "", log)
		internalCertHash := enterprisepem.ReadHashFromSecret(ctx, r.SecretClient, mrs.Namespace, mrsConfig.InternalClusterSecretName, "", log)
		log.Debugf("Creating StatefulSet %s with %d replicas in cluster: %s", mrs.MultiStatefulsetName(clusterNum), replicasThisReconciliation, item.ClusterName)

		stsOverride := appsv1.StatefulSetSpec{}
		if item.StatefulSetConfiguration != nil {
			stsOverride = item.StatefulSetConfiguration.SpecWrapper.Spec
		}

		var automationAgentVersion string
		if architectures.IsRunningStaticArchitecture(mrs.Annotations) {
			if !mrs.Spec.IsAgentImageOverridden() {
				automationAgentVersion, err = r.getAgentVersion(conn, conn.OpsManagerVersion().VersionString, false, log)
				if err != nil {
					log.Errorf("Impossible to get agent version, please override the agent image by providing a pod template")
					status := workflow.Failed(xerrors.Errorf("Failed to get agent version: %w", err))
					return status
				}
			}
		}

		opts := mconstruct.MultiClusterReplicaSetOptions(
			mconstruct.WithClusterNum(clusterNum),
			Replicas(replicasThisReconciliation),
			mconstruct.WithStsOverride(&stsOverride),
			mconstruct.WithAnnotations(mrs.Name, certHash),
			mconstruct.WithServiceName(mrs.MultiHeadlessServiceName(clusterNum)),
			PodEnvVars(newPodVars(conn, projectConfig, mrs.Spec.LogLevel)),
			CurrentAgentAuthMechanism(currentAgentAuthMode),
			CertificateHash(certHash),
			InternalClusterHash(internalCertHash),
			WithLabels(mrs.GetOwnerLabels()),
			WithAdditionalMongodConfig(mrs.Spec.GetAdditionalMongodConfig()),
			WithInitDatabaseNonStaticImage(images.ContainerImage(r.imageUrls, util.InitDatabaseImageUrlEnv, r.initDatabaseNonStaticImageVersion)),
			WithDatabaseNonStaticImage(images.ContainerImage(r.imageUrls, util.NonStaticDatabaseEnterpriseImage, r.databaseNonStaticImageVersion)),
			WithAgentImage(images.ContainerImage(r.imageUrls, architectures.MdbAgentImageRepo, automationAgentVersion)),
			WithMongodbImage(images.GetOfficialImage(r.imageUrls, mrs.Spec.Version, mrs.GetAnnotations())),
		)

		sts := mconstruct.MultiClusterStatefulSet(*mrs, opts)
		deleteSts, err := shouldDeleteStatefulSet(*mrs, item)
		if err != nil {
			return workflow.Failed(xerrors.Errorf("failed to create StatefulSet in cluster: %s, err: %w", item.ClusterName, err))
		}

		if deleteSts {
			if err := memberClient.Delete(ctx, &sts); err != nil && !apiErrors.IsNotFound(err) {
				return workflow.Failed(xerrors.Errorf("failed to delete StatefulSet in cluster: %s, err: %w", item.ClusterName, err))
			}
			continue
		}

		workflowStatus := create.HandlePVCResize(ctx, memberClient, &sts, log)
		if !workflowStatus.IsOK() {
			return workflowStatus
		}
		if err = r.updateStatusFromInnerMethod(ctx, mrs, log, workflowStatus); err != nil {
			return workflow.Failed(xerrors.Errorf("%w", err))
		}

		_, err = statefulset.CreateOrUpdateStatefulset(ctx, memberClient, mrs.Namespace, log, &sts)
		if err != nil {
			return workflow.Failed(xerrors.Errorf("failed to create/update StatefulSet in cluster: %s, err: %w", item.ClusterName, err))
		}

		processes := automationConfig.Deployment.GetAllProcessNames()
		// If we don't have processes defined yet, that means we are in the first deployment, and we can deploy all
		// stateful-sets in parallel.
		// If we have processes defined, it means we want to wait until all of them are ready.
		if len(processes) > 0 {
			// We already have processes defined, and therefore we are waiting for each of them
			if status := statefulset.GetStatefulSetStatus(ctx, sts.Namespace, sts.Name, memberClient); !status.IsOK() {
				return status
			}

			log.Infof("Successfully ensured StatefulSet in cluster: %s", item.ClusterName)
		} else {
			// We create all sts in parallel and wait below for all of them to finish
			stsLocators = append(stsLocators, stsIdentifier{
				namespace:   sts.Namespace,
				name:        sts.Name,
				client:      memberClient,
				clusterName: item.ClusterName,
			})
		}
	}

	// Running into this means we are in the first deployment/don't have processes yet.
	// That means we have created them in parallel and now waiting for them to get ready.
	for _, locator := range stsLocators {
		if status := statefulset.GetStatefulSetStatus(ctx, locator.namespace, locator.name, locator.client); !status.IsOK() {
			return status
		}
		log.Infof("Successfully ensured StatefulSet in cluster: %s", locator.clusterName)
	}

	return workflow.OK()
}

// updateStatusFromInnerMethod ensures to only update the status if it has been updated.
// Since spec.Mapping is just a cache, it would be replaced; therefore, we need to cache it
func (r *ReconcileMongoDbMultiReplicaSet) updateStatusFromInnerMethod(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, log *zap.SugaredLogger, workflowStatus workflow.Status) error {
	// if there are no pvc changes, then we don't need to update the status
	if !workflow.ContainsPVCOption(workflowStatus.StatusOptions()) {
		return nil
	}
	tmpMapping := mrs.Spec.Mapping
	_, err := r.updateStatus(ctx, mrs, workflow.Pending(""), log, workflowStatus.StatusOptions()...)
	mrs.Spec.Mapping = tmpMapping
	if err != nil {
		return err
	}
	return nil
}

// shouldDeleteStatefulSet returns a boolean value indicating whether the StatefulSet associated with
// the given cluster spec item should be deleted or not.
func shouldDeleteStatefulSet(mrs mdbmultiv1.MongoDBMultiCluster, item mdb.ClusterSpecItem) (bool, error) {
	for _, specItem := range mrs.Spec.ClusterSpecList {
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
func getMembersForClusterSpecItemThisReconciliation(mrs *mdbmultiv1.MongoDBMultiCluster, item mdb.ClusterSpecItem) (int, error) {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return -1, err
	}
	for _, clusterItem := range clusterSpecList {
		if clusterItem.ClusterName == item.ClusterName {
			return clusterItem.Members, nil
		}
	}
	return -1, xerrors.Errorf("did not find %s in cluster spec list", item.ClusterName)
}

// saveLastAchievedSpec updates the MongoDBMultiCluster resource with the spec that was just achieved.
func (r *ReconcileMongoDbMultiReplicaSet) saveLastAchievedSpec(ctx context.Context, mrs mdbmultiv1.MongoDBMultiCluster) error {
	clusterSpecs, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}

	lastAchievedSpec := mrs.Spec
	lastAchievedSpec.ClusterSpecList = clusterSpecs
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

	return annotations.SetAnnotations(ctx, &mrs, annotationsToAdd, r.client)
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (r *ReconcileMongoDbMultiReplicaSet) updateOmDeploymentRs(ctx context.Context, conn om.Connection, mrs mdbmultiv1.MongoDBMultiCluster, isRecovering bool, log *zap.SugaredLogger) error {
	reachableHostnames := make([]string, 0)

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		// When failing to retrieve the list of failed clusters we proceed assuming there are no failed clusters,
		// but log the error as it indicates a malformed annotation.
		log.Errorf("failed retrieving list of failed clusters: %s", err.Error())
	}
	for _, spec := range clusterSpecList {
		hostnamesToAdd := dns.GetMultiClusterProcessHostnames(mrs.Name, mrs.Namespace, mrs.ClusterNum(spec.ClusterName), spec.Members, mrs.Spec.GetClusterDomain(), mrs.Spec.GetExternalDomainForMemberCluster(spec.ClusterName))
		if stringutil.Contains(failedClusterNames, spec.ClusterName) {
			log.Debugf("Skipping hostnames %+v as they are part of the failed cluster %s ", hostnamesToAdd, spec.ClusterName)
			continue
		}
		if mrs.GetClusterSpecByName(spec.ClusterName) == nil {
			log.Debugf("Skipping hostnames %+v as they are part of a cluster not known by the operator %s ", hostnamesToAdd, spec.ClusterName)
			continue
		}
		reachableHostnames = append(reachableHostnames, hostnamesToAdd...)
	}

	err = agents.WaitForRsAgentsToRegisterSpecifiedHostnames(conn, reachableHostnames, log)
	if err != nil && !isRecovering {
		return err
	}

	existingDeployment, err := conn.ReadDeployment()
	if err != nil {
		return err
	}

	processIds := getReplicaSetProcessIdsFromReplicaSets(mrs.Name, existingDeployment)
	log.Debugf("Existing process Ids: %+v", processIds)

	certificateFileName := ""
	internalClusterPath := ""

	// If tls is enabled we need to configure the "processes" array in opsManager/Cloud Manager with the
	// correct certFilePath, with the new tls design, this path has the certHash in it(so that cert can be rotated
	// without pod restart), we can get the cert hash from any of the statefulset, here we pick the statefulset in the first cluster.
	if mrs.Spec.Security.IsTLSEnabled() {
		firstStatefulSet, err := r.firstStatefulSet(ctx, &mrs)
		if err != nil {
			return err
		}

		if hash := firstStatefulSet.Annotations[util.InternalCertAnnotationKey]; hash != "" {
			internalClusterPath = fmt.Sprintf("%s%s", util.InternalClusterAuthMountPath, hash)
		}

		if certificateHash := firstStatefulSet.Annotations[certs.CertHashAnnotationKey]; certificateHash != "" {
			certificateFileName = fmt.Sprintf("%s/%s", util.TLSCertMountPath, certificateHash)
		}
	}

	processes, err := process.CreateMongodProcessesWithLimitMulti(r.imageUrls[mcoConstruct.MongodbImageEnv], r.forceEnterprise, mrs, certificateFileName)
	if err != nil && !isRecovering {
		return err
	}

	if len(processes) != len(mrs.Spec.GetMemberOptions()) {
		log.Warnf("the number of member options is different than the number of mongod processes to be created: %d processes - %d replica set member options", len(processes), len(mrs.Spec.GetMemberOptions()))
	}
	rs := om.NewMultiClusterReplicaSetWithProcesses(om.NewReplicaSet(mrs.Name, mrs.Spec.Version), processes, mrs.Spec.GetMemberOptions(), processIds, mrs.Spec.Connectivity)

	caFilePath := fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)

	// We do not provide an agentCertSecretName on purpose because then we will default to the non pem secret on the central cluster.
	// Below method has special code handling reading certificates from the central cluster in that case.
	status, additionalReconciliationRequired := r.updateOmAuthentication(ctx, conn, rs.GetProcessNames(), &mrs, "", caFilePath, internalClusterPath, isRecovering, log)
	if !status.IsOK() && !isRecovering {
		return xerrors.Errorf("failed to enable Authentication for MongoDB Multi Replicaset")
	}

	lastMongodbConfig := mrs.GetLastAdditionalMongodConfig()

	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			return ReconcileReplicaSetAC(ctx, d, mrs.Spec.DbCommonSpec, lastMongodbConfig, mrs.Name, rs, caFilePath, internalClusterPath, nil, log)
		},
		log,
	)
	if err != nil && !isRecovering {
		return err
	}

	reconcileResult, err := ReconcileLogRotateSetting(conn, mrs.Spec.Agent, log)
	if !reconcileResult.IsOK() {
		return xerrors.Errorf("failed to configure logrotation for MongoDBMultiCluster RS, err: %w", err)
	}

	if additionalReconciliationRequired {
		return xerrors.Errorf("failed to complete reconciliation")
	}

	status = r.ensureBackupConfigurationAndUpdateStatus(ctx, conn, &mrs, r.SecretClient, log)
	if !status.IsOK() && !isRecovering {
		return xerrors.Errorf("failed to configure backup for MongoDBMultiCluster RS")
	}

	reachableProcessNames := make([]string, 0)
	for _, proc := range rs.Processes {
		if stringutil.Contains(reachableHostnames, proc.HostName()) {
			reachableProcessNames = append(reachableProcessNames, proc.Name())
		}
	}
	if err := om.WaitForReadyState(conn, reachableProcessNames, isRecovering, log); err != nil && !isRecovering {
		return err
	}
	return nil
}

func getReplicaSetProcessIdsFromReplicaSets(replicaSetName string, deployment om.Deployment) map[string]int {
	processIds := map[string]int{}

	replicaSet := deployment.GetReplicaSetByName(replicaSetName)
	if replicaSet == nil {
		return map[string]int{}
	}

	for _, m := range replicaSet.Members() {
		processIds[m.Name()] = m.Id()
	}

	return processIds
}

func getSRVService(mrs *mdbmultiv1.MongoDBMultiCluster) corev1.Service {
	additionalConfig := mrs.Spec.GetAdditionalMongodConfig()
	port := additionalConfig.GetPortOrDefault()

	svc := service.Builder().
		SetName(fmt.Sprintf("%s-svc", mrs.Name)).
		SetNamespace(mrs.Namespace).
		SetSelector(mconstruct.PodLabel(mrs.Name)).
		SetLabels(mrs.GetOwnerLabels()).
		SetPublishNotReadyAddresses(true).
		AddPort(&corev1.ServicePort{Port: port, Name: "mongodb"}).
		AddPort(&corev1.ServicePort{Port: create.GetNonEphemeralBackupPort(port), Name: "backup", TargetPort: intstr.IntOrString{IntVal: create.GetNonEphemeralBackupPort(port)}}).
		Build()

	return svc
}

func getExternalService(mrs *mdbmultiv1.MongoDBMultiCluster, clusterName string, podNum int) corev1.Service {
	clusterNum := mrs.ClusterNum(clusterName)

	svc := getService(mrs, clusterName, podNum)
	svc.Name = dns.GetMultiExternalServiceName(mrs.GetName(), clusterNum, podNum)
	svc.Spec.Type = corev1.ServiceTypeLoadBalancer

	externalAccessConfig := mrs.Spec.GetExternalAccessConfigurationForMemberCluster(clusterName)
	if externalAccessConfig != nil {
		// first we override with the Service spec from the root and then from a specific cluster.
		if mrs.Spec.ExternalAccessConfiguration != nil {
			globalOverrideSpecWrapper := mrs.Spec.ExternalAccessConfiguration.ExternalService.SpecWrapper
			if globalOverrideSpecWrapper != nil {
				svc.Spec = merge.ServiceSpec(svc.Spec, globalOverrideSpecWrapper.Spec)
			}
		}
		clusterLevelOverrideSpec := externalAccessConfig.ExternalService.SpecWrapper
		additionalAnnotations := externalAccessConfig.ExternalService.Annotations
		if clusterLevelOverrideSpec != nil {
			svc.Spec = merge.ServiceSpec(svc.Spec, clusterLevelOverrideSpec.Spec)
		}
		svc.Annotations = merge.StringToStringMap(svc.Annotations, additionalAnnotations)
	}

	return svc
}

func getService(mrs *mdbmultiv1.MongoDBMultiCluster, clusterName string, podNum int) corev1.Service {
	svcLabels := map[string]string{
		appsv1.StatefulSetPodNameLabel: dns.GetMultiPodName(mrs.Name, mrs.ClusterNum(clusterName), podNum),
	}
	svcLabels = merge.StringToStringMap(svcLabels, mrs.GetOwnerLabels())

	labelSelectors := map[string]string{
		appsv1.StatefulSetPodNameLabel: dns.GetMultiPodName(mrs.Name, mrs.ClusterNum(clusterName), podNum),
		util.OperatorLabelName:         util.OperatorName,
	}

	additionalConfig := mrs.Spec.GetAdditionalMongodConfig()
	port := additionalConfig.GetPortOrDefault()

	svc := service.Builder().
		SetName(dns.GetMultiServiceName(mrs.Name, mrs.ClusterNum(clusterName), podNum)).
		SetNamespace(mrs.Namespace).
		SetSelector(labelSelectors).
		SetLabels(svcLabels).
		SetPublishNotReadyAddresses(true).
		AddPort(&corev1.ServicePort{Port: port, Name: "mongodb"}).
		// Note: in the agent-launcher.sh We explicitly pass an offset of 1. When port N is exposed
		// the agent would use port N+1 for the spinning up of the ephemeral mongod process, which is used for backup
		AddPort(&corev1.ServicePort{Port: create.GetNonEphemeralBackupPort(port), Name: "backup", TargetPort: intstr.IntOrString{IntVal: create.GetNonEphemeralBackupPort(port)}}).
		Build()

	return svc
}

// reconcileServices makes sure that we have a service object corresponding to each statefulset pod
// in the member clusters
func (r *ReconcileMongoDbMultiReplicaSet) reconcileServices(ctx context.Context, log *zap.SugaredLogger, mrs *mdbmultiv1.MongoDBMultiCluster) error {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		log.Errorf("failed retrieving list of failed clusters: %s", err.Error())
	}

	for _, e := range clusterSpecList {
		if stringutil.Contains(failedClusterNames, e.ClusterName) {
			log.Warnf(fmt.Sprintf("cluster %s is marked as failed", e.ClusterName))
			continue
		}

		client, ok := r.memberClusterClientsMap[e.ClusterName]
		if !ok {
			log.Warnf(fmt.Sprintf("cluster %s missing from client map", e.ClusterName))
			continue
		}
		if e.Members == 0 {
			log.Warnf("skipping services creation: no members assigned to cluster %s", e.ClusterName)
			continue
		}

		// ensure SRV service
		srvService := getSRVService(mrs)
		if err := ensureSRVService(ctx, client, srvService, e.ClusterName); err != nil {
			return err
		}
		log.Infof("Successfully created SRV service %s in cluster %s", srvService.Name, e.ClusterName)

		// ensure Headless service
		headlessServiceName := mrs.MultiHeadlessServiceName(mrs.ClusterNum(e.ClusterName))
		nameSpacedName := kube.ObjectKey(mrs.Namespace, headlessServiceName)
		headlessService := create.BuildService(nameSpacedName, mrs, ptr.To(headlessServiceName), nil, mrs.Spec.AdditionalMongodConfig.GetPortOrDefault(), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
		if err := ensureHeadlessService(ctx, client, headlessService, e.ClusterName); err != nil {
			return err
		}
		log.Infof("Successfully created headless service %s in cluster: %s", headlessServiceName, e.ClusterName)
	}

	// by default, we would create the duplicate services
	shouldCreateDuplicates := mrs.Spec.DuplicateServiceObjects == nil || *mrs.Spec.DuplicateServiceObjects
	for memberClusterName, memberClusterClient := range r.memberClusterClientsMap {
		if stringutil.Contains(failedClusterNames, memberClusterName) {
			log.Warnf(fmt.Sprintf("cluster %s is marked as failed, skipping creation of services", memberClusterName))
			continue
		}

		for _, clusterSpecItem := range clusterSpecList {
			if !shouldCreateDuplicates && clusterSpecItem.ClusterName != memberClusterName {
				// skip creating of other cluster's services (duplicates) in the current cluster
				continue
			}

			if err := ensureServices(ctx, memberClusterClient, memberClusterName, mrs, clusterSpecItem, log); err != nil {
				return err
			}
		}
	}

	return nil
}

func ensureSRVService(ctx context.Context, client service.GetUpdateCreator, svc corev1.Service, clusterName string) error {
	err := mekoService.CreateOrUpdateService(ctx, client, svc)
	if err != nil && !apiErrors.IsAlreadyExists(err) {
		return xerrors.Errorf("failed to create SRV service: % in cluster: %s, err: %w", svc.Name, clusterName, err)
	}
	return nil
}

// ensureServices creates pod services and/or external services.
// If externalAccess is defined (at spec or clusterSpecItem level) then we always create an external service.
// If externalDomain is defined then we DO NOT create pod services (service created for each pod selecting only 1 pod).
// When there are external domains used, we don't use internal pod-service FQDNs as hostnames at all,
// so there is no point in creating pod services.
// But when external domains are not used, then mongod process hostnames use pod service FQDN, and
// at the same time user might want to expose externally using external services.
func ensureServices(ctx context.Context, client service.GetUpdateCreator, clientClusterName string, m *mdbmultiv1.MongoDBMultiCluster, clusterSpecItem mdb.ClusterSpecItem, log *zap.SugaredLogger) error {
	for podNum := 0; podNum < clusterSpecItem.Members; podNum++ {
		var svc corev1.Service
		if m.Spec.GetExternalAccessConfigurationForMemberCluster(clusterSpecItem.ClusterName) != nil {
			svc = getExternalService(m, clusterSpecItem.ClusterName, podNum)
			externalDomain := m.Spec.GetExternalDomainForMemberCluster(clusterSpecItem.ClusterName)
			placeholderReplacer := create.GetMultiClusterMongoDBPlaceholderReplacer(m.Name, m.Name, m.Namespace, clusterSpecItem.ClusterName, m.ClusterNum(clusterSpecItem.ClusterName), externalDomain, m.Spec.ClusterDomain, podNum)
			if processedAnnotations, replacedFlag, err := placeholderReplacer.ProcessMap(svc.Annotations); err != nil {
				return xerrors.Errorf("failed to process annotations in external service %s in cluster %s: %w", svc.Name, clientClusterName, err)
			} else if replacedFlag {
				log.Debugf("Replaced placeholders in annotations in external service %s in cluster: %s. Annotations before: %+v, annotations after: %+v", svc.Name, clientClusterName, svc.Annotations, processedAnnotations)
				svc.Annotations = processedAnnotations
			}

			err := mekoService.CreateOrUpdateService(ctx, client, svc)
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				return xerrors.Errorf("failed to create external service %s in cluster: %s, err: %w", svc.Name, clientClusterName, err)
			}
		}

		// we create regular pod-services only if we don't use external domains
		if m.Spec.GetExternalDomainForMemberCluster(clusterSpecItem.ClusterName) == nil {
			svc = getService(m, clusterSpecItem.ClusterName, podNum)
			err := mekoService.CreateOrUpdateService(ctx, client, svc)
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				return xerrors.Errorf("failed to create pod service %s in cluster: %s, err: %w", svc.Name, clientClusterName, err)
			}
		}
	}
	return nil
}

func ensureHeadlessService(ctx context.Context, client service.GetUpdateCreator, svc corev1.Service, clusterName string) error {
	err := mekoService.CreateOrUpdateService(ctx, client, svc)
	if err != nil && !apiErrors.IsAlreadyExists(err) {
		return xerrors.Errorf("failed to create headless service: %s in cluster: %s, err: %w", svc.Name, clusterName, err)
	}
	return nil
}

func getHostnameOverrideConfigMap(mrs mdbmultiv1.MongoDBMultiCluster, clusterNum int, clusterName string, members int) corev1.ConfigMap {
	data := make(map[string]string)

	externalDomain := mrs.Spec.GetExternalDomainForMemberCluster(clusterName)
	for podNum := 0; podNum < members; podNum++ {
		key := dns.GetMultiPodName(mrs.Name, clusterNum, podNum)
		data[key] = dns.GetMultiClusterPodServiceFQDN(mrs.Name, mrs.Namespace, clusterNum, externalDomain, podNum, mrs.Spec.GetClusterDomain())
	}

	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-hostname-override", mrs.Name),
			Namespace: mrs.Namespace,
			Labels:    mrs.GetOwnerLabels(),
		},
		Data: data,
	}
	return cm
}

func (r *ReconcileMongoDbMultiReplicaSet) reconcileHostnameOverrideConfigMap(ctx context.Context, log *zap.SugaredLogger, mrs mdbmultiv1.MongoDBMultiCluster) error {
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
		cm := getHostnameOverrideConfigMap(mrs, i, e.ClusterName, e.Members)

		err = configmap.CreateOrUpdate(ctx, client, cm)
		if err != nil && !apiErrors.IsAlreadyExists(err) {
			return xerrors.Errorf("failed to create configmap: %s in cluster: %s, err: %w", cm.Name, e.ClusterName, err)
		}
		log.Infof("Successfully ensured configmap: %s in cluster: %s", cm.Name, e.ClusterName)

	}
	return nil
}

func (r *ReconcileMongoDbMultiReplicaSet) reconcileOMCAConfigMap(ctx context.Context, log *zap.SugaredLogger, mrs mdbmultiv1.MongoDBMultiCluster, configMapName string) error {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}
	failedClusterNames, err := mrs.GetFailedClusterNames()
	if err != nil {
		log.Warnf("failed retrieving list of failed clusters: %s", err.Error())
	}

	cm, err := r.client.GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, configMapName))
	if err != nil {
		return err
	}
	for _, clusterSpecItem := range clusterSpecList {
		if stringutil.Contains(failedClusterNames, clusterSpecItem.ClusterName) {
			log.Warnf("failed to create configmap %s: cluster %s is marked as failed", configMapName, clusterSpecItem.ClusterName)
			continue
		}
		client := r.memberClusterClientsMap[clusterSpecItem.ClusterName]
		memberCm := configmap.Builder().SetName(configMapName).SetNamespace(mrs.Namespace).SetData(cm.Data).Build()
		err := configmap.CreateOrUpdate(ctx, client, memberCm)
		if err != nil && !apiErrors.IsAlreadyExists(err) {
			return xerrors.Errorf("failed to create configmap: %s in cluster %s, err: %w", cm.Name, clusterSpecItem.ClusterName, err)
		}
		log.Infof("Sucessfully ensured configmap: %s in cluster: %s", cm.Name, clusterSpecItem.ClusterName)
	}
	return nil
}

// AddMultiReplicaSetController creates a new MongoDbMultiReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddMultiReplicaSetController(ctx context.Context, mgr manager.Manager, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise bool, memberClustersMap map[string]cluster.Cluster) error {
	// Create a new controller
	reconciler := newMultiClusterReplicaSetReconciler(ctx, mgr.GetClient(), imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, om.NewOpsManagerConnection, multicluster.ClustersMapToClientMap(memberClustersMap))
	c, err := controller.New(util.MongoDbMultiClusterController, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}) // nolint:forbidigo
	if err != nil {
		return err
	}

	eventHandler := ResourceEventHandler{deleter: reconciler}
	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbmultiv1.MongoDBMultiCluster{}, &eventHandler, predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mdbmultiv1.MongoDBMultiCluster)
			newResource := e.ObjectNew.(*mdbmultiv1.MongoDBMultiCluster)

			oldSpecAnnotation := oldResource.GetAnnotations()[util.LastAchievedSpec]
			newSpecAnnotation := newResource.GetAnnotations()[util.LastAchievedSpec]

			// don't handle an update to just the previous spec annotation if they are not the same.
			// this prevents the operator triggering reconciliations on resource that it is updating itself.
			if !reflect.DeepEqual(oldSpecAnnotation, newSpecAnnotation) {
				return false
			}

			return reflect.DeepEqual(oldResource.GetStatus(), newResource.GetStatus())
		},
	}))
	if err != nil {
		return err
	}

	err = c.Watch(source.Channel[client.Object](OmUpdateChannel, &handler.EnqueueRequestForObject{}))
	if err != nil {
		return xerrors.Errorf("not able to setup OmUpdateChannel to listent to update events from OM: %s", err)
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.Secret{},
		&watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	// register watcher across member clusters
	for k, v := range memberClustersMap {
		err := c.Watch(source.Kind[client.Object](v.GetCache(), &appsv1.StatefulSet{}, &khandler.EnqueueRequestForOwnerMultiCluster{}, watch.PredicatesForMultiStatefulSet()))
		if err != nil {
			return xerrors.Errorf("failed to set Watch on member cluster: %s, err: %w", k, err)
		}
	}

	// the operator watches the member clusters' API servers to determine whether the clusters are healthy or not
	eventChannel := make(chan event.GenericEvent)
	memberClusterHealthChecker := memberwatch.MemberClusterHealthChecker{Cache: make(map[string]*memberwatch.MemberHeathCheck)}
	go memberClusterHealthChecker.WatchMemberClusterHealth(ctx, zap.S(), eventChannel, reconciler.client, memberClustersMap)

	err = c.Watch(source.Channel[client.Object](eventChannel, &handler.EnqueueRequestForObject{}))
	if err != nil {
		zap.S().Errorf("failed to watch for member cluster healthcheck: %s", err)
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.ConfigMap{},
		watch.ConfigMapEventHandler{
			ConfigMapName:      util.MemberListConfigMapName,
			ConfigMapNamespace: env.ReadOrPanic(util.CurrentNamespace), // nolint:forbidigo
		},
		predicate.ResourceVersionChangedPredicate{},
	))
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbMultiReplicaSetController)
	return err
}

// OnDelete cleans up Ops Manager state and all Kubernetes resources associated with this instance.
func (r *ReconcileMongoDbMultiReplicaSet) OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error {
	mrs := obj.(*mdbmultiv1.MongoDBMultiCluster)
	return r.deleteManagedResources(ctx, *mrs, log)
}

// cleanOpsManagerState removes the project configuration (processes, auth settings etc.) from the corresponding OM project.
func (r *ReconcileMongoDbMultiReplicaSet) cleanOpsManagerState(ctx context.Context, mrs mdbmultiv1.MongoDBMultiCluster, log *zap.SugaredLogger) error {
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.client, r.SecretClient, &mrs, log)
	if err != nil {
		return err
	}

	log.Infow("Removing replica set from Ops Manager", "config", mrs.Spec)
	conn, _, err := connection.PrepareOpsManagerConnection(ctx, r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, mrs.Namespace, log)
	if err != nil {
		return err
	}

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			processNames = d.GetProcessNames(om.ReplicaSet{}, mrs.Name)
			// error means that replica set is not in the deployment - it's ok, and we can proceed (could happen if
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

// deleteManagedResources deletes resources across all member clusters that are owned by this MongoDBMultiCluster resource.
func (r *ReconcileMongoDbMultiReplicaSet) deleteManagedResources(ctx context.Context, mrs mdbmultiv1.MongoDBMultiCluster, log *zap.SugaredLogger) error {
	var errs error
	if err := r.cleanOpsManagerState(ctx, mrs, log); err != nil {
		errs = multierror.Append(errs, err)
	}

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return err
	}

	for _, item := range clusterSpecList {
		clusterName := item.ClusterName
		clusterClient := r.memberClusterClientsMap[clusterName]
		if err := r.deleteClusterResources(ctx, clusterClient, clusterName, &mrs, log); err != nil {
			errs = multierror.Append(errs, xerrors.Errorf("failed deleting dependant resources in cluster %s: %w", clusterName, err))
		}
	}

	return errs
}

// filterClusterSpecItem filters items out of a list based on provided predicate.
func filterClusterSpecItem(items mdb.ClusterSpecList, fn func(item mdb.ClusterSpecItem) bool) mdb.ClusterSpecList {
	var result mdb.ClusterSpecList
	for _, item := range items {
		if fn(item) {
			result = append(result, item)
		}
	}
	return result
}

func sortClusterSpecList(clusterSpecList mdb.ClusterSpecList) {
	sort.SliceStable(clusterSpecList, func(i, j int) bool {
		return clusterSpecList[i].ClusterName < clusterSpecList[j].ClusterName
	})
}

func clusterSpecListsEqual(effective, desired mdb.ClusterSpecList) bool {
	comparer := cmp.Comparer(func(x, y automationconfig.MemberOptions) bool {
		return true
	})
	return cmp.Equal(effective, desired, comparer)
}
