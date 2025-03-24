package operator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"syscall"

	"github.com/blang/semver"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/api"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connectionstring"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/annotations"
	kubernetesClient "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/constants"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/images"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/identifiable"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault/vaultwatcher"
)

var OmUpdateChannel chan event.GenericEvent

const (
	oldestSupportedOpsManagerVersion = "5.0.0"
	programmaticKeyVersion           = "5.0.0"
)

type S3ConfigGetter interface {
	GetAuthenticationModes() []string
	GetResourceName() string
	BuildConnectionString(username, password string, scheme connectionstring.Scheme, connectionParams map[string]string) string
}

// OpsManagerReconciler is a controller implementation.
// It's Reconciler function is called by Controller Runtime.
// WARNING: do not put any mutable state into this class. Controller runtime uses and shares a single instance of it.
type OpsManagerReconciler struct {
	*ReconcileCommonController
	omInitializer          api.Initializer
	omAdminProvider        api.AdminProvider
	omConnectionFactory    om.ConnectionFactory
	oldestSupportedVersion semver.Version
	programmaticKeyVersion semver.Version

	memberClustersMap map[string]client.Client

	imageUrls                  images.ImageUrls
	initAppdbVersion           string
	initOpsManagerImageVersion string
}

var _ reconcile.Reconciler = &OpsManagerReconciler{}

func NewOpsManagerReconciler(ctx context.Context, kubeClient client.Client, memberClustersMap map[string]client.Client, imageUrls images.ImageUrls, initAppdbVersion, initOpsManagerImageVersion string, omFunc om.ConnectionFactory, initializer api.Initializer, adminProvider api.AdminProvider) *OpsManagerReconciler {
	return &OpsManagerReconciler{
		ReconcileCommonController:  NewReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:        omFunc,
		omInitializer:              initializer,
		omAdminProvider:            adminProvider,
		oldestSupportedVersion:     semver.MustParse(oldestSupportedOpsManagerVersion),
		programmaticKeyVersion:     semver.MustParse(programmaticKeyVersion),
		memberClustersMap:          memberClustersMap,
		imageUrls:                  imageUrls,
		initAppdbVersion:           initAppdbVersion,
		initOpsManagerImageVersion: initOpsManagerImageVersion,
	}
}

type OMDeploymentState struct {
	CommonDeploymentState `json:",inline"`
}

func NewOMDeploymentState() *OMDeploymentState {
	return &OMDeploymentState{CommonDeploymentState{ClusterMapping: map[string]int{}}}
}

// OpsManagerReconcilerHelper is a type containing the state and logic for a SINGLE reconcile execution.
// This object is NOT shared between multiple reconcile invocations in contrast to OpsManagerReconciler where
// we cannot store state of the current reconcile.
type OpsManagerReconcilerHelper struct {
	opsManager      *omv1.MongoDBOpsManager
	memberClusters  []multicluster.MemberCluster
	stateStore      *StateStore[OMDeploymentState]
	deploymentState *OMDeploymentState
}

func NewOpsManagerReconcilerHelper(ctx context.Context, opsManagerReconciler *OpsManagerReconciler, opsManager *omv1.MongoDBOpsManager, globalMemberClustersMap map[string]client.Client, log *zap.SugaredLogger) (*OpsManagerReconcilerHelper, error) {
	reconcilerHelper := OpsManagerReconcilerHelper{
		opsManager: opsManager,
	}

	if !opsManager.Spec.IsMultiCluster() {
		reconcilerHelper.memberClusters = []multicluster.MemberCluster{multicluster.GetLegacyCentralMemberCluster(opsManager.Spec.Replicas, 0, opsManagerReconciler.client, opsManagerReconciler.SecretClient)}
		return &reconcilerHelper, nil
	}

	if len(globalMemberClustersMap) == 0 {
		return nil, xerrors.Errorf("member clusters have to be initialized for MultiCluster OpsManager topology")
	}

	// here we access ClusterSpecList directly, as we have to check what's been defined in yaml
	if len(opsManager.Spec.ClusterSpecList) == 0 {
		return nil, xerrors.Errorf("for MongoDBOpsManager.spec.Topology = MultiCluster, clusterSpecList has to be non empty")
	}

	clusterNamesFromClusterSpecList := util.Transform(opsManager.GetClusterSpecList(), func(clusterSpecItem omv1.ClusterSpecOMItem) string {
		return clusterSpecItem.ClusterName
	})
	if err := reconcilerHelper.initializeStateStore(ctx, opsManagerReconciler, opsManager, globalMemberClustersMap, log, clusterNamesFromClusterSpecList); err != nil {
		return nil, xerrors.Errorf("failed to initialize OM state store: %w", err)
	}

	for _, clusterSpecItem := range opsManager.GetClusterSpecList() {
		var memberClusterKubeClient kubernetesClient.Client
		var memberClusterSecretClient secrets.SecretClient
		memberClusterClient, ok := globalMemberClustersMap[clusterSpecItem.ClusterName]
		if !ok {
			var clusterList []string
			for m := range globalMemberClustersMap {
				clusterList = append(clusterList, m)
			}
			log.Warnf("Member cluster %s specified in clusterSpecList is not found in the list of operator's member clusters: %+v. "+
				"Assuming the cluster is down. It will be ignored from reconciliation.", clusterSpecItem.ClusterName, clusterList)
		} else {
			memberClusterKubeClient = kubernetesClient.NewClient(memberClusterClient)
			memberClusterSecretClient = secrets.SecretClient{
				VaultClient: nil, // Vault is not supported yet on multicluster
				KubeClient:  memberClusterKubeClient,
			}
		}

		reconcilerHelper.memberClusters = append(reconcilerHelper.memberClusters, multicluster.MemberCluster{
			Name:         clusterSpecItem.ClusterName,
			Index:        reconcilerHelper.deploymentState.ClusterMapping[clusterSpecItem.ClusterName],
			Client:       memberClusterKubeClient,
			SecretClient: memberClusterSecretClient,
			// TODO should we do lastAppliedMember map like in AppDB?
			Replicas: clusterSpecItem.Members,
			Active:   true,
			Healthy:  memberClusterKubeClient != nil,
		})
	}

	return &reconcilerHelper, nil
}

func (r *OpsManagerReconcilerHelper) initializeStateStore(ctx context.Context, reconciler *OpsManagerReconciler, opsManager *omv1.MongoDBOpsManager, globalMemberClustersMap map[string]client.Client, log *zap.SugaredLogger, clusterNamesFromClusterSpecList []string) error {
	r.deploymentState = NewOMDeploymentState()

	r.stateStore = NewStateStore[OMDeploymentState](opsManager.Namespace, opsManager.Name, reconciler.client)
	if err := r.stateStore.read(ctx); err != nil {
		if apiErrors.IsNotFound(err) {
			// If the deployment state config map is missing, then it might be either:
			//  - fresh deployment
			//  - existing deployment, but it's a first reconcile on the operator version with the new deployment state
			//  - existing deployment, but for some reason the deployment state config map has been deleted
			// In all cases, the deployment config map will be recreated from the state we're keeping and maintaining in
			// the old place (in annotations, spec.status, config maps) in order to allow for the downgrade of the operator.
			if err := r.migrateToNewDeploymentState(ctx, opsManager, reconciler.client); err != nil {
				return err
			}
			// Here we don't use saveOMState wrapper, as we don't need to write the legacy state
			if err := r.stateStore.WriteState(ctx, r.deploymentState, log); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	if state, err := r.stateStore.ReadState(ctx); err != nil {
		return err
	} else {
		r.deploymentState = state
	}

	r.deploymentState.ClusterMapping = multicluster.AssignIndexesForMemberClusterNames(r.deploymentState.ClusterMapping, clusterNamesFromClusterSpecList)

	if err := r.saveOMState(ctx, opsManager, reconciler.client, log); err != nil {
		return err
	}

	return nil
}

func (r *OpsManagerReconcilerHelper) saveOMState(ctx context.Context, spec *omv1.MongoDBOpsManager, client kubernetesClient.Client, log *zap.SugaredLogger) error {
	if err := r.stateStore.WriteState(ctx, r.deploymentState, log); err != nil {
		return err
	}
	if err := r.writeLegacyStateConfigMap(ctx, spec, client, log); err != nil {
		return err
	}
	return nil
}

// writeLegacyStateConfigMap converts the DeploymentState to the legacy Config Map and write it to the cluster
func (r *OpsManagerReconcilerHelper) writeLegacyStateConfigMap(ctx context.Context, spec *omv1.MongoDBOpsManager, client kubernetesClient.Client, log *zap.SugaredLogger) error {
	// ClusterMapping ConfigMap
	mappingConfigMapData := map[string]string{}
	for k, v := range r.deploymentState.ClusterMapping {
		mappingConfigMapData[k] = fmt.Sprintf("%d", v)
	}
	mappingConfigMap := configmap.Builder().SetName(spec.ClusterMappingConfigMapName()).SetNamespace(spec.Namespace).SetData(mappingConfigMapData).Build()
	if err := configmap.CreateOrUpdate(ctx, client, mappingConfigMap); err != nil {
		return xerrors.Errorf("failed to update cluster mapping configmap %s: %w", spec.ClusterMappingConfigMapName(), err)
	}
	log.Debugf("Saving cluster mapping configmap %s: %v", spec.ClusterMappingConfigMapName(), mappingConfigMapData)

	return nil
}

func (r *OpsManagerReconcilerHelper) GetMemberClusters() []multicluster.MemberCluster {
	return r.memberClusters
}

func (r *OpsManagerReconcilerHelper) getHealthyMemberClusters() []multicluster.MemberCluster {
	var healthyMemberClusters []multicluster.MemberCluster
	for i := 0; i < len(r.memberClusters); i++ {
		if r.memberClusters[i].Healthy {
			healthyMemberClusters = append(healthyMemberClusters, r.memberClusters[i])
		}
	}

	return healthyMemberClusters
}

type backupDaemonFQDN struct {
	hostname          string
	memberClusterName string
}

// BackupDaemonHeadlessFQDNs returns headless FQDNs for backup daemons for all member clusters.
// It's used primarily for registering backup daemon instances in Ops Manager.
func (r *OpsManagerReconcilerHelper) BackupDaemonHeadlessFQDNs() []backupDaemonFQDN {
	var fqdns []backupDaemonFQDN
	for _, memberCluster := range r.GetMemberClusters() {
		clusterHostnames, _ := dns.GetDNSNames(r.BackupDaemonStatefulSetNameForMemberCluster(memberCluster), r.BackupDaemonHeadlessServiceNameForMemberCluster(memberCluster),
			r.opsManager.Namespace, r.opsManager.Spec.GetClusterDomain(), r.BackupDaemonMembersForMemberCluster(memberCluster), nil)
		for _, hostname := range clusterHostnames {
			fqdns = append(fqdns, backupDaemonFQDN{hostname: hostname, memberClusterName: memberCluster.Name})
		}

	}

	return fqdns
}

func (r *OpsManagerReconcilerHelper) BackupDaemonMembersForMemberCluster(memberCluster multicluster.MemberCluster) int {
	clusterSpecOMItem := r.getClusterSpecOMItem(memberCluster.Name)
	if clusterSpecOMItem.Backup != nil {
		return clusterSpecOMItem.Backup.Members
	}
	return 0
}

func (r *OpsManagerReconcilerHelper) OpsManagerMembersForMemberCluster(memberCluster multicluster.MemberCluster) int {
	clusterSpecOMItem := r.getClusterSpecOMItem(memberCluster.Name)
	return clusterSpecOMItem.Members
}

func (r *OpsManagerReconcilerHelper) getClusterSpecOMItem(clusterName string) omv1.ClusterSpecOMItem {
	idx := slices.IndexFunc(r.opsManager.GetClusterSpecList(), func(clusterSpecOMItem omv1.ClusterSpecOMItem) bool {
		return clusterSpecOMItem.ClusterName == clusterName
	})
	if idx == -1 {
		panic(fmt.Errorf("member cluster %s not found in OM's clusterSpecList", clusterName))
	}

	return r.opsManager.GetClusterSpecList()[idx]
}

func (r *OpsManagerReconcilerHelper) OpsManagerStatefulSetNameForMemberCluster(memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return r.opsManager.Name
	}

	return fmt.Sprintf("%s-%d", r.opsManager.Name, memberCluster.Index)
}

func (r *OpsManagerReconcilerHelper) BackupDaemonStatefulSetNameForMemberCluster(memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return r.opsManager.BackupDaemonStatefulSetName()
	}

	return r.opsManager.BackupDaemonStatefulSetNameForClusterIndex(memberCluster.Index)
}

func (r *OpsManagerReconcilerHelper) BackupDaemonHeadlessServiceNameForMemberCluster(memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return r.opsManager.BackupDaemonServiceName()
	}

	return r.opsManager.BackupDaemonHeadlessServiceNameForClusterIndex(memberCluster.Index)
}

func (r *OpsManagerReconcilerHelper) BackupDaemonPodServiceNameForMemberCluster(memberCluster multicluster.MemberCluster) []string {
	if memberCluster.Legacy {
		hostnames, _ := dns.GetDNSNames(r.BackupDaemonStatefulSetNameForMemberCluster(memberCluster), r.BackupDaemonHeadlessServiceNameForMemberCluster(memberCluster),
			r.opsManager.Namespace, r.opsManager.Spec.GetClusterDomain(), r.BackupDaemonMembersForMemberCluster(memberCluster), nil)
		return hostnames
	}

	var hostnames []string
	for podIdx := 0; podIdx < r.BackupDaemonMembersForMemberCluster(memberCluster); podIdx++ {
		hostnames = append(hostnames, fmt.Sprintf("%s-%d-svc", r.BackupDaemonStatefulSetNameForMemberCluster(memberCluster), podIdx))
	}

	return hostnames
}

func (r *OpsManagerReconcilerHelper) migrateToNewDeploymentState(ctx context.Context, om *omv1.MongoDBOpsManager, centralClient kubernetesClient.Client) error {
	legacyMemberClusterMapping, err := getLegacyMemberClusterMapping(ctx, om.Namespace, om.ClusterMappingConfigMapName(), centralClient)
	if apiErrors.IsNotFound(err) || !om.Spec.IsMultiCluster() {
		legacyMemberClusterMapping = map[string]int{}
	} else if err != nil {
		return err
	}

	r.deploymentState.ClusterMapping = legacyMemberClusterMapping

	return nil
}

// +kubebuilder:rbac:groups=mongodb.com,resources={opsmanagers,opsmanagers/status,opsmanagers/finalizers},verbs=*,namespace=placeholder

// Reconcile performs the reconciliation logic for AppDB, Ops Manager and Backup
// AppDB is reconciled first (independent of Ops Manager as the agent is run in headless mode) and
// Ops Manager statefulset is created then.
// Backup daemon statefulset is created/updated and configured optionally if backup is enabled.
// Note, that the pointer to ops manager resource is used in 'Reconcile' method as resource status is mutated
// many times during reconciliation, and It's important to keep updates to avoid status override
func (r *OpsManagerReconciler) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("OpsManager", request.NamespacedName)

	opsManager := &omv1.MongoDBOpsManager{}

	opsManagerExtraStatusParams := mdbstatus.NewOMPartOption(mdbstatus.OpsManager)

	if reconcileResult, err := r.readOpsManagerResource(ctx, request, opsManager, log); err != nil {
		return reconcileResult, err
	}

	log.Info("-> OpsManager.Reconcile")
	log.Infow("OpsManager.Spec", "spec", opsManager.Spec)
	log.Infow("OpsManager.Status", "status", opsManager.Status)

	// We perform this check here and not inside the validation because we don't want to put OM in failed state
	// just log the error and put in the "Unsupported" state
	semverVersion, err := versionutil.StringToSemverVersion(opsManager.Spec.Version)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Invalid("%s is not a valid version", opsManager.Spec.Version), log, opsManagerExtraStatusParams)
	}
	if semverVersion.LT(r.oldestSupportedVersion) {
		return r.updateStatus(ctx, opsManager, workflow.Unsupported("Ops Manager Version %s is not supported by this version of the operator. Please upgrade to a version >=%s", opsManager.Spec.Version, oldestSupportedOpsManagerVersion), log, opsManagerExtraStatusParams)
	}

	// AppDB Reconciler will be created with a nil OmAdmin, which is set below after initialization
	appDbReconciler, err := r.createNewAppDBReconciler(ctx, opsManager, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error initializing AppDB reconciler: %w", err)), log, opsManagerExtraStatusParams)
	}

	if part, err := opsManager.ProcessValidationsOnReconcile(); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Invalid("%s", err.Error()), log, mdbstatus.NewOMPartOption(part))
	}

	acClient := appDbReconciler.getMemberCluster(appDbReconciler.getNameOfFirstMemberCluster()).Client
	if err := ensureResourcesForArchitectureChange(ctx, acClient, r.SecretClient, opsManager); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error ensuring resources for upgrade from 1 to 3 container AppDB: %w", err)), log, opsManagerExtraStatusParams)
	}

	if err := ensureSharedGlobalResources(ctx, r.client, opsManager); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error ensuring shared global resources %w", err)), log, opsManagerExtraStatusParams)
	}

	// 1. Reconcile AppDB
	emptyResult, _ := workflow.OK().ReconcileResult()
	retryResult := reconcile.Result{Requeue: true}

	// TODO: make SetupCommonWatchers support opsmanager watcher setup
	// The order matters here, since appDB and opsManager share the same reconcile ObjectKey being opsmanager crd
	// That means we need to remove first, which SetupCommonWatchers does, then register additional watches
	appDBReplicaSet := opsManager.Spec.AppDB
	r.SetupCommonWatchers(&appDBReplicaSet, nil, nil, appDBReplicaSet.GetName())

	// We need to remove the watches on the top of the reconcile since we might add resources with the same key below.
	if opsManager.IsTLSEnabled() {
		r.resourceWatcher.RegisterWatchedTLSResources(opsManager.ObjectKey(), opsManager.Spec.GetOpsManagerCA(), []string{opsManager.TLSCertificateSecretName()})
	}
	// register backup
	r.watchMongoDBResourcesReferencedByBackup(ctx, opsManager, log)

	result, err := appDbReconciler.ReconcileAppDB(ctx, opsManager)
	if err != nil || (result != emptyResult && result != retryResult) {
		return result, err
	}

	appDBPassword, err := appDbReconciler.ensureAppDbPassword(ctx, opsManager, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error getting AppDB password: %w", err)), log, opsManagerExtraStatusParams)
	}

	opsManagerReconcilerHelper, err := NewOpsManagerReconcilerHelper(ctx, r, opsManager, r.memberClustersMap, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(err), log, opsManagerExtraStatusParams)
	}

	appDBConnectionString := buildMongoConnectionUrl(opsManager, appDBPassword, appDbReconciler.getCurrentStatefulsetHostnames(opsManager))
	for _, memberCluster := range opsManagerReconcilerHelper.getHealthyMemberClusters() {
		if err := r.ensureAppDBConnectionStringInMemberCluster(ctx, opsManager, appDBConnectionString, memberCluster, log); err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("error ensuring AppDB connection string in cluster %s: %w", memberCluster.Name, err)), log, opsManagerExtraStatusParams)
		}
	}

	initOpsManagerImage := images.ContainerImage(r.imageUrls, util.InitOpsManagerImageUrl, r.initOpsManagerImageVersion)
	opsManagerImage := images.ContainerImage(r.imageUrls, util.OpsManagerImageUrl, opsManager.Spec.Version)

	// 2. Reconcile Ops Manager
	status, omAdmin := r.reconcileOpsManager(ctx, opsManagerReconcilerHelper, opsManager, appDBConnectionString, initOpsManagerImage, opsManagerImage, log)
	if !status.IsOK() {
		return r.updateStatus(ctx, opsManager, status, log, opsManagerExtraStatusParams, mdbstatus.NewBaseUrlOption(opsManager.CentralURL()))
	}

	// the AppDB still needs to configure monitoring, now that Ops Manager has been created
	// we can finish this configuration.
	if result.Requeue {
		log.Infof("Requeuing reconciliation to configure AppDB monitoring in Ops Manager.")
		return result, nil
	}

	// 3. Reconcile Backup Daemon
	if status := r.reconcileBackupDaemon(ctx, opsManagerReconcilerHelper, opsManager, omAdmin, appDBConnectionString, initOpsManagerImage, opsManagerImage, log); !status.IsOK() {
		return r.updateStatus(ctx, opsManager, status, log, mdbstatus.NewOMPartOption(mdbstatus.Backup))
	}

	annotationsToAdd, err := getAnnotationsForOpsManagerResource(opsManager)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(err), log)
	}

	if vault.IsVaultSecretBackend() {
		vaultMap := make(map[string]string)
		for _, s := range opsManager.GetSecretsMountedIntoPod() {
			path := fmt.Sprintf("%s/%s/%s", r.VaultClient.OpsManagerSecretMetadataPath(), appDBReplicaSet.Namespace, s)
			vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		}
		for _, s := range opsManager.Spec.AppDB.GetSecretsMountedIntoPod() {
			path := fmt.Sprintf("%s/%s/%s", r.VaultClient.AppDBSecretMetadataPath(), appDBReplicaSet.Namespace, s)
			vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		}

		for k, val := range vaultMap {
			annotationsToAdd[k] = val
		}
	}

	if err := annotations.SetAnnotations(ctx, opsManager, annotationsToAdd, r.client); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(err), log)
	}
	// All statuses are updated by now - we don't need to update any others - just return
	log.Info("Finished reconciliation for MongoDbOpsManager!")
	// success
	return workflow.OK().ReconcileResult()
}

// ensureSharedGlobalResources ensures that resources that are shared across watched namespaces (e.g. secrets) are in sync
func ensureSharedGlobalResources(ctx context.Context, secretGetUpdaterCreator secret.GetUpdateCreator, opsManager *omv1.MongoDBOpsManager) error {
	operatorNamespace := env.ReadOrPanic(util.CurrentNamespace) // nolint:forbidigo
	if operatorNamespace == opsManager.Namespace {
		// nothing to sync, OM runs in the same namespace as the operator
		return nil
	}

	if imagePullSecretsName, found := env.Read(util.ImagePullSecrets); found { // nolint:forbidigo
		imagePullSecrets, err := secretGetUpdaterCreator.GetSecret(ctx, kube.ObjectKey(operatorNamespace, imagePullSecretsName))
		if err != nil {
			return err
		}

		omNsSecret := secret.Builder().
			SetName(imagePullSecretsName).
			SetNamespace(opsManager.Namespace).
			SetByteData(imagePullSecrets.Data).
			Build()
		omNsSecret.Type = imagePullSecrets.Type
		if err := createOrUpdateSecretIfNotFound(ctx, secretGetUpdaterCreator, omNsSecret); err != nil {
			return err
		}
	}

	return nil
}

// ensureResourcesForArchitectureChange ensures that the new resources expected to be present.
func ensureResourcesForArchitectureChange(ctx context.Context, acSecretClient, secretGetUpdaterCreator secret.GetUpdateCreator, opsManager *omv1.MongoDBOpsManager) error {
	acSecret, err := acSecretClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.AutomationConfigSecretName()))
	// if the automation config does not exist, we are not upgrading from an existing deployment. We can create everything from scratch.
	if err != nil {
		if !secrets.SecretNotExist(err) {
			return xerrors.Errorf("error getting existing automation config secret: %w", err)
		}
		return nil
	}

	ac, err := automationconfig.FromBytes(acSecret.Data[automationconfig.ConfigKey])
	if err != nil {
		return xerrors.Errorf("error unmarshalling existing automation: %w", err)
	}

	// the Ops Manager user should always exist within the automation config.
	var omUser automationconfig.MongoDBUser
	for _, authUser := range ac.Auth.Users {
		if authUser.Username == util.OpsManagerMongoDBUserName {
			omUser = authUser
			break
		}
	}

	if omUser.Username == "" {
		return xerrors.Errorf("ops manager user not present in the automation config")
	}

	err = createOrUpdateSecretIfNotFound(ctx, secretGetUpdaterCreator, secret.Builder().
		SetName(opsManager.Spec.AppDB.OpsManagerUserScramCredentialsName()).
		SetNamespace(opsManager.Namespace).
		SetField("sha1-salt", omUser.ScramSha1Creds.Salt).
		SetField("sha-1-server-key", omUser.ScramSha1Creds.ServerKey).
		SetField("sha-1-stored-key", omUser.ScramSha1Creds.StoredKey).
		SetField("sha256-salt", omUser.ScramSha256Creds.Salt).
		SetField("sha-256-server-key", omUser.ScramSha256Creds.ServerKey).
		SetField("sha-256-stored-key", omUser.ScramSha256Creds.StoredKey).
		Build())
	if err != nil {
		return xerrors.Errorf("failed to create/update scram credentials secret for Ops Manager user: %w", err)
	}

	// ensure that the agent password stays consistent with what it was previously
	err = createOrUpdateSecretIfNotFound(ctx, secretGetUpdaterCreator, secret.Builder().
		SetName(opsManager.Spec.AppDB.GetAgentPasswordSecretNamespacedName().Name).
		SetNamespace(opsManager.Spec.AppDB.GetAgentPasswordSecretNamespacedName().Namespace).
		SetField(constants.AgentPasswordKey, ac.Auth.AutoPwd).
		Build())
	if err != nil {
		return xerrors.Errorf("failed to create/update password secret for agent user: %w", err)
	}

	// ensure that the keyfile stays consistent with what it was previously
	err = createOrUpdateSecretIfNotFound(ctx, secretGetUpdaterCreator, secret.Builder().
		SetName(opsManager.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name).
		SetNamespace(opsManager.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Namespace).
		SetField(constants.AgentKeyfileKey, ac.Auth.Key).
		Build())
	if err != nil {
		return xerrors.Errorf("failed to create/update keyfile secret for agent user: %w", err)
	}

	// there was a rename for a specific secret, `om-resource-db-password -> om-resource-db-om-password`
	// this was done as now there are multiple secrets associated with the AppDB, and the contents of this old one correspond to the Ops Manager user.
	oldOpsManagerUserPasswordSecret, err := secretGetUpdaterCreator.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()+"-password"))
	if err != nil {
		// if it's not there, we don't want to create it. We only want to create the new secret if it is present.
		if secrets.SecretNotExist(err) {
			return nil
		}
		return err
	}

	return secret.CreateOrUpdate(ctx, secretGetUpdaterCreator, secret.Builder().
		SetNamespace(opsManager.Namespace).
		SetName(opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName()).
		SetByteData(oldOpsManagerUserPasswordSecret.Data).
		Build(),
	)
}

// createOrUpdateSecretIfNotFound creates the given secret if it does not exist.
func createOrUpdateSecretIfNotFound(ctx context.Context, secretGetUpdaterCreator secret.GetUpdateCreator, desiredSecret corev1.Secret) error {
	_, err := secretGetUpdaterCreator.GetSecret(ctx, kube.ObjectKey(desiredSecret.Namespace, desiredSecret.Name))
	if err != nil {
		if secrets.SecretNotExist(err) {
			return secret.CreateOrUpdate(ctx, secretGetUpdaterCreator, desiredSecret)
		}
		return xerrors.Errorf("error getting secret %s/%s: %w", desiredSecret.Namespace, desiredSecret.Name, err)
	}
	return nil
}

func (r *OpsManagerReconciler) reconcileOpsManager(ctx context.Context, reconcilerHelper *OpsManagerReconcilerHelper, opsManager *omv1.MongoDBOpsManager, appDBConnectionString, initOpsManagerImage, opsManagerImage string, log *zap.SugaredLogger) (workflow.Status, api.OpsManagerAdmin) {
	var genKeySecretMap map[string][]byte
	var err error
	if genKeySecretMap, err = r.ensureGenKeyInOperatorCluster(ctx, opsManager, log); err != nil {
		return workflow.Failed(xerrors.Errorf("error in ensureGenKeyInOperatorCluster: %w", err)), nil
	}

	if err := r.replicateGenKeyInMemberClusters(ctx, reconcilerHelper, genKeySecretMap); err != nil {
		return workflow.Failed(xerrors.Errorf("error in replicateGenKeyInMemberClusters: %w", err)), nil
	}

	if err := r.replicateTLSCAInMemberClusters(ctx, reconcilerHelper); err != nil {
		return workflow.Failed(xerrors.Errorf("error in replicateTLSCAInMemberClusters: %w", err)), nil
	}

	if err := r.replicateAppDBTLSCAInMemberClusters(ctx, reconcilerHelper); err != nil {
		return workflow.Failed(xerrors.Errorf("error in replicateAppDBTLSCAInMemberClusters: %w", err)), nil
	}

	if err := r.replicateKMIPCAInMemberClusters(ctx, reconcilerHelper); err != nil {
		return workflow.Failed(xerrors.Errorf("error in replicateKMIPCAInMemberClusters: %w", err)), nil
	}

	if err := r.replicateQueryableBackupTLSSecretInMemberClusters(ctx, reconcilerHelper); err != nil {
		return workflow.Failed(xerrors.Errorf("error in replicateQueryableBackupTLSSecretInMemberClusters: %w", err)), nil
	}

	if err := r.replicateLogBackInMemberClusters(ctx, reconcilerHelper); err != nil {
		return workflow.Failed(xerrors.Errorf("error in replicateQueryableBackupTLSSecretInMemberClusters: %w", err)), nil
	}

	// Prepare Ops Manager StatefulSets in parallel in all member clusters
	for _, memberCluster := range reconcilerHelper.getHealthyMemberClusters() {
		status := r.createOpsManagerStatefulsetInMemberCluster(ctx, reconcilerHelper, appDBConnectionString, memberCluster, initOpsManagerImage, opsManagerImage, log)
		if !status.IsOK() {
			return status, nil
		}
	}

	// wait for all statefulsets to become ready
	var statefulSetStatus workflow.Status = workflow.OK()
	for _, memberCluster := range reconcilerHelper.getHealthyMemberClusters() {
		status := getStatefulSetStatus(ctx, opsManager.Namespace, reconcilerHelper.OpsManagerStatefulSetNameForMemberCluster(memberCluster), memberCluster.Client)
		statefulSetStatus = statefulSetStatus.Merge(status)
	}
	if !statefulSetStatus.IsOK() {
		return statefulSetStatus, nil
	}

	opsManagerURL := opsManager.CentralURL()

	// 3. Prepare Ops Manager (ensure the first user is created and public API key saved to secret)
	var omAdmin api.OpsManagerAdmin
	var status workflow.Status
	if status, omAdmin = r.prepareOpsManager(ctx, opsManager, opsManagerURL, log); !status.IsOK() {
		return status, nil
	}

	// 4. Trigger agents upgrade if necessary
	if err := triggerOmChangedEventIfNeeded(ctx, opsManager, r.client, log); err != nil {
		log.Warn("Not triggering an Ops Manager version changed event: %s", err)
	}

	// 5. Stop backup daemon if necessary
	if err := r.stopBackupDaemonIfNeeded(ctx, reconcilerHelper); err != nil {
		return workflow.Failed(err), nil
	}

	statusOptions := []mdbstatus.Option{mdbstatus.NewOMPartOption(mdbstatus.OpsManager), mdbstatus.NewBaseUrlOption(opsManagerURL)}
	if _, err := r.updateStatus(ctx, opsManager, workflow.OK(), log, statusOptions...); err != nil {
		return workflow.Failed(err), nil
	}

	return status, omAdmin
}

// triggerOmChangedEventIfNeeded triggers upgrade process for all the MongoDB agents in the system if the major/minor version upgrade
// happened for Ops Manager
func triggerOmChangedEventIfNeeded(ctx context.Context, opsManager *omv1.MongoDBOpsManager, c kubernetesClient.Client, log *zap.SugaredLogger) error {
	if opsManager.Spec.Version == opsManager.Status.OpsManagerStatus.Version || opsManager.Status.OpsManagerStatus.Version == "" {
		return nil
	}
	newVersion, err := versionutil.StringToSemverVersion(opsManager.Spec.Version)
	if err != nil {
		return xerrors.Errorf("failed to parse Ops Manager version %s: %w", opsManager.Spec.Version, err)
	}
	oldVersion, err := versionutil.StringToSemverVersion(opsManager.Status.OpsManagerStatus.Version)
	if err != nil {
		return xerrors.Errorf("failed to parse Ops Manager status version %s: %w", opsManager.Status.OpsManagerStatus.Version, err)
	}
	if newVersion.Major != oldVersion.Major || newVersion.Minor != oldVersion.Minor {
		log.Infof("Ops Manager version has upgraded from %s to %s - scheduling the upgrade for all the Agents in the system", oldVersion, newVersion)
		if architectures.IsRunningStaticArchitecture(opsManager.Annotations) {
			mdbList := &mdbv1.MongoDBList{}
			err := c.List(ctx, mdbList)
			if err != nil {
				return err
			}
			for _, m := range mdbList.Items {
				OmUpdateChannel <- event.GenericEvent{Object: &m}
			}

			multiList := &mdbmulti.MongoDBMultiClusterList{}
			err = c.List(ctx, multiList)
			if err != nil {
				return err
			}
			for _, m := range multiList.Items {
				OmUpdateChannel <- event.GenericEvent{Object: &m}
			}

		} else {
			// This is a noop in static-architecture world
			agents.ScheduleUpgrade()
		}
	}

	return nil
}

// stopBackupDaemonIfNeeded stops the backup daemon when OM is upgraded.
// Otherwise, the backup daemon will remain in a broken state (because of version missmatch between OM and backup daemon)
// due to this STS limitation: https://github.com/kubernetes/kubernetes/issues/67250.
// Later, the normal reconcile process will update the STS and start the backup daemon.
func (r *OpsManagerReconciler) stopBackupDaemonIfNeeded(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper) error {
	opsManager := reconcileHelper.opsManager
	if opsManager.Spec.Version == opsManager.Status.OpsManagerStatus.Version || opsManager.Status.OpsManagerStatus.Version == "" {
		return nil
	}

	for _, memberCluster := range reconcileHelper.getHealthyMemberClusters() {
		if _, err := r.scaleStatefulSet(ctx, opsManager.Namespace, reconcileHelper.BackupDaemonStatefulSetNameForMemberCluster(memberCluster), 0, memberCluster.Client); client.IgnoreNotFound(err) != nil {
			return err
		}

		// delete all backup daemon pods, scaling down the statefulSet to 0 does not terminate the pods,
		// if the number of pods is greater than 1 and all of them are in an unhealthy state
		cleanupOptions := mdbv1.MongodbCleanUpOptions{
			Namespace: opsManager.Namespace,
			Labels: map[string]string{
				"app": reconcileHelper.BackupDaemonHeadlessServiceNameForMemberCluster(memberCluster),
			},
		}

		if err := memberCluster.Client.DeleteAllOf(ctx, &corev1.Pod{}, &cleanupOptions); client.IgnoreNotFound(err) != nil {
			return err
		}
	}

	return nil
}

func (r *OpsManagerReconciler) reconcileBackupDaemon(ctx context.Context, reconcilerHelper *OpsManagerReconcilerHelper, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, appDBConnectionString, initOpsManagerImage, opsManagerImage string, log *zap.SugaredLogger) workflow.Status {
	backupStatusPartOption := mdbstatus.NewOMPartOption(mdbstatus.Backup)

	// If backup is not enabled, we check whether it is still configured in OM to update the status.
	if !opsManager.Spec.Backup.Enabled {
		var backupStatus workflow.Status
		backupStatus = workflow.Disabled()

		for _, fqdn := range reconcilerHelper.BackupDaemonHeadlessFQDNs() {
			// In case there is a backup daemon running still, while backup is not enabled, we check whether it is still configured in OM.
			// We're keeping the `Running` status if still configured and marking it as `Disabled` otherwise.
			backupStatus = workflow.OK()
			_, err := omAdmin.ReadDaemonConfig(fqdn.hostname, util.PvcMountPathHeadDb)
			if apierror.NewNonNil(err).ErrorBackupDaemonConfigIsNotFound() || errors.Is(err, syscall.ECONNREFUSED) {
				backupStatus = workflow.Disabled()
				break
			}
		}

		_, err := r.updateStatus(ctx, opsManager, backupStatus, log, backupStatusPartOption)
		if err != nil {
			return workflow.Failed(err)
		}
		return backupStatus
	}

	for _, memberCluster := range reconcilerHelper.getHealthyMemberClusters() {
		// Prepare Backup Daemon StatefulSet (create and wait)
		if status := r.createBackupDaemonStatefulset(ctx, reconcilerHelper, appDBConnectionString, memberCluster, initOpsManagerImage, opsManagerImage, log); !status.IsOK() {
			return status
		}
	}

	// Configure Backup using API
	if status := r.prepareBackupInOpsManager(ctx, reconcilerHelper, opsManager, omAdmin, appDBConnectionString, log); !status.IsOK() {
		return status
	}

	// StatefulSet will reach ready state eventually once backup has been configured in Ops Manager.

	// wait for all statefulsets to become ready
	for _, memberCluster := range reconcilerHelper.getHealthyMemberClusters() {
		if status := getStatefulSetStatus(ctx, opsManager.Namespace, reconcilerHelper.BackupDaemonStatefulSetNameForMemberCluster(memberCluster), memberCluster.Client); !status.IsOK() {
			return status
		}
	}

	if _, err := r.updateStatus(ctx, opsManager, workflow.OK(), log, backupStatusPartOption); err != nil {
		return workflow.Failed(err)
	}

	return workflow.OK()
}

// readOpsManagerResource reads Ops Manager Custom resource into pointer provided
func (r *OpsManagerReconciler) readOpsManagerResource(ctx context.Context, request reconcile.Request, ref *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (reconcile.Result, error) {
	if result, err := r.getResource(ctx, request, ref, log); err != nil {
		return result, err
	}
	// Reset warnings so that they are not stale, will populate accurate warnings in reconciliation
	ref.SetWarnings([]mdbstatus.Warning{}, mdbstatus.NewOMPartOption(mdbstatus.OpsManager), mdbstatus.NewOMPartOption(mdbstatus.AppDb), mdbstatus.NewOMPartOption(mdbstatus.Backup))
	return reconcile.Result{}, nil
}

// ensureAppDBConnectionString ensures that the AppDB Connection String exists in a secret.
func (r *OpsManagerReconciler) ensureAppDBConnectionStringInMemberCluster(ctx context.Context, opsManager *omv1.MongoDBOpsManager, computedConnectionString string, memberCluster multicluster.MemberCluster, log *zap.SugaredLogger) error {
	var opsManagerSecretPath string
	if r.VaultClient != nil {
		opsManagerSecretPath = r.VaultClient.OpsManagerSecretPath()
	}

	_, err := memberCluster.SecretClient.ReadSecret(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.AppDBMongoConnectionStringSecretName()), opsManagerSecretPath)
	if err != nil {
		if secrets.SecretNotExist(err) {
			log.Debugf("AppDB connection string secret was not found in cluster %s, creating %s now", memberCluster.Name, kube.ObjectKey(opsManager.Namespace, opsManager.AppDBMongoConnectionStringSecretName()))
			// assume the secret was not found, need to create it

			connectionStringSecret := secret.Builder().
				SetName(opsManager.AppDBMongoConnectionStringSecretName()).
				SetNamespace(opsManager.Namespace).
				SetField(util.AppDbConnectionStringKey, computedConnectionString).
				Build()

			return memberCluster.SecretClient.PutSecret(ctx, connectionStringSecret, opsManagerSecretPath)
		}
		log.Warnf("Error getting connection string secret: %s", err)
		return err
	}

	connectionStringSecretData := map[string]string{
		util.AppDbConnectionStringKey: computedConnectionString,
	}
	connectionStringSecret := secret.Builder().
		SetName(opsManager.AppDBMongoConnectionStringSecretName()).
		SetNamespace(opsManager.Namespace).
		SetStringMapToData(connectionStringSecretData).Build()
	log.Debugf("Connection string secret already exists, updating %s", kube.ObjectKey(opsManager.Namespace, opsManager.AppDBMongoConnectionStringSecretName()))
	return memberCluster.SecretClient.PutSecret(ctx, connectionStringSecret, opsManagerSecretPath)
}

func hashConnectionString(connectionString string) string {
	bytes := []byte(connectionString)
	hashBytes := sha256.Sum256(bytes)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:])
}

func (r *OpsManagerReconciler) createOpsManagerStatefulsetInMemberCluster(ctx context.Context, reconcilerHelper *OpsManagerReconcilerHelper, appDBConnectionString string, memberCluster multicluster.MemberCluster, initOpsManagerImage, opsManagerImage string, log *zap.SugaredLogger) workflow.Status {
	opsManager := reconcilerHelper.opsManager

	r.ensureConfiguration(reconcilerHelper, log)

	var vaultConfig vault.VaultConfiguration
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
	}

	clusterSpecItem := reconcilerHelper.getClusterSpecOMItem(memberCluster.Name)
	sts, err := construct.OpsManagerStatefulSet(ctx, r.SecretClient, opsManager, memberCluster, log,
		construct.WithInitOpsManagerImage(initOpsManagerImage),
		construct.WithOpsManagerImage(opsManagerImage),
		construct.WithConnectionStringHash(hashConnectionString(appDBConnectionString)),
		construct.WithVaultConfig(vaultConfig),
		construct.WithKmipConfig(ctx, opsManager, memberCluster.Client, log),
		construct.WithStsOverride(clusterSpecItem.GetStatefulSetSpecOverride()),
		construct.WithReplicas(reconcilerHelper.OpsManagerMembersForMemberCluster(memberCluster)),
	)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("error building OpsManager stateful set: %w", err))
	}

	if err := create.OpsManagerInKubernetes(ctx, memberCluster.Client, opsManager, sts, log); err != nil {
		return workflow.Failed(err)
	}

	return workflow.OK()
}

func AddOpsManagerController(ctx context.Context, mgr manager.Manager, memberClustersMap map[string]cluster.Cluster, imageUrls images.ImageUrls, initAppdbVersion, initOpsManagerImageVersion string) error {
	reconciler := NewOpsManagerReconciler(ctx, mgr.GetClient(), multicluster.ClustersMapToClientMap(memberClustersMap), imageUrls, initAppdbVersion, initOpsManagerImageVersion, om.NewOpsManagerConnection, &api.DefaultInitializer{}, api.NewOmAdmin)
	c, err := controller.New(util.MongoDbOpsManagerController, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}) // nolint:forbidigo
	if err != nil {
		return err
	}

	// watch for changes to the Ops Manager resources
	eventHandler := MongoDBOpsManagerEventHandler{reconciler: reconciler}

	if err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &omv1.MongoDBOpsManager{}, &eventHandler, watch.PredicatesForOpsManager())); err != nil {
		return err
	}

	// watch the secret with the Ops Manager user password
	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.Secret{},
		&watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.Secret{},
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbv1.MongoDB{},
		&watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	// if vault secret backend is enabled watch for Vault secret change and trigger reconcile
	if vault.IsVaultSecretBackend() {
		eventChannel := make(chan event.GenericEvent)
		go vaultwatcher.WatchSecretChangeForOM(ctx, zap.S(), eventChannel, reconciler.client, reconciler.VaultClient)

		err = c.Watch(source.Channel[client.Object](eventChannel, &handler.EnqueueRequestForObject{}))
		if err != nil {
			zap.S().Errorf("Failed to watch for vault secret changes: %v", err)
		}
	}
	zap.S().Infof("Registered controller %s", util.MongoDbOpsManagerController)
	return nil
}

// ensureConfiguration makes sure the mandatory configuration is specified.
func (r *OpsManagerReconciler) ensureConfiguration(reconcilerHelper *OpsManagerReconcilerHelper, log *zap.SugaredLogger) {
	// update the central URL
	setConfigProperty(reconcilerHelper.opsManager, util.MmsCentralUrlPropKey, reconcilerHelper.opsManager.CentralURL(), log)

	if reconcilerHelper.opsManager.Spec.AppDB.Security.IsTLSEnabled() {
		setConfigProperty(reconcilerHelper.opsManager, util.MmsMongoSSL, "true", log)
	}
	if reconcilerHelper.opsManager.Spec.AppDB.GetCAConfigMapName() != "" {
		setConfigProperty(reconcilerHelper.opsManager, util.MmsMongoCA, omv1.GetAppDBCaPemPath(), log)
	}

	// override the versions directory (defaults to "/opt/mongodb/mms/mongodb-releases/")
	setConfigProperty(reconcilerHelper.opsManager, util.MmsVersionsDirectory, "/mongodb-ops-manager/mongodb-releases/", log)

	// feature controls will always be enabled
	setConfigProperty(reconcilerHelper.opsManager, util.MmsFeatureControls, "true", log)

	if reconcilerHelper.opsManager.Spec.Backup.QueryableBackupSecretRef.Name != "" {
		setConfigProperty(reconcilerHelper.opsManager, util.BrsQueryablePem, "/certs/queryable.pem", log)
	}
}

// createBackupDaemonStatefulset creates a StatefulSet for backup daemon and waits shortly until it's started
// Note, that the idea of creating two statefulsets for Ops Manager and Backup Daemon in parallel hasn't worked out
// as the daemon in this case just hangs silently (in practice it's ok to start it in ~1 min after start of OM though
// we will just start them sequentially)
func (r *OpsManagerReconciler) createBackupDaemonStatefulset(ctx context.Context, reconcilerHelper *OpsManagerReconcilerHelper, appDBConnectionString string, memberCluster multicluster.MemberCluster, initOpsManagerImage, opsManagerImage string, log *zap.SugaredLogger) workflow.Status {
	if !reconcilerHelper.opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	if err := r.ensureAppDBConnectionStringInMemberCluster(ctx, reconcilerHelper.opsManager, appDBConnectionString, memberCluster, log); err != nil {
		return workflow.Failed(err)
	}

	r.ensureConfiguration(reconcilerHelper, log)

	var vaultConfig vault.VaultConfiguration
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
	}
	clusterSpecItem := reconcilerHelper.getClusterSpecOMItem(memberCluster.Name)
	sts, err := construct.BackupDaemonStatefulSet(ctx, r.SecretClient, reconcilerHelper.opsManager, memberCluster, log,
		construct.WithInitOpsManagerImage(initOpsManagerImage),
		construct.WithOpsManagerImage(opsManagerImage),
		construct.WithConnectionStringHash(hashConnectionString(appDBConnectionString)),
		construct.WithVaultConfig(vaultConfig),
		// TODO KMIP support will not work across clusters
		construct.WithKmipConfig(ctx, reconcilerHelper.opsManager, memberCluster.Client, log),
		construct.WithStsOverride(clusterSpecItem.GetBackupStatefulSetSpecOverride()),
		construct.WithReplicas(reconcilerHelper.BackupDaemonMembersForMemberCluster(memberCluster)),
	)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("error building stateful set: %w", err))
	}

	needToRequeue, err := create.BackupDaemonInKubernetes(ctx, memberCluster.Client, reconcilerHelper.opsManager, sts, log)
	if err != nil {
		return workflow.Failed(err)
	}
	if needToRequeue {
		return workflow.OK().Requeue()
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) watchMongoDBResourcesReferencedByKmip(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) {
	if !opsManager.Spec.IsKmipEnabled() {
		return
	}

	mdbList := &mdbv1.MongoDBList{}
	err := r.client.List(ctx, mdbList)
	if err != nil {
		log.Warnf("failed to fetch MongoDBList from Kubernetes: %v", err)
	}

	for _, m := range mdbList.Items {
		if m.Spec.Backup != nil && m.Spec.Backup.IsKmipEnabled() {
			r.resourceWatcher.AddWatchedResourceIfNotAdded(
				m.Name,
				m.Namespace,
				watch.MongoDB,
				kube.ObjectKeyFromApiObject(opsManager))

			r.resourceWatcher.AddWatchedResourceIfNotAdded(
				m.Spec.Backup.Encryption.Kmip.Client.ClientCertificateSecretName(m.GetName()),
				opsManager.Namespace,
				watch.Secret,
				kube.ObjectKeyFromApiObject(opsManager))

			r.resourceWatcher.AddWatchedResourceIfNotAdded(
				m.Spec.Backup.Encryption.Kmip.Client.ClientCertificatePasswordSecretName(m.GetName()),
				opsManager.Namespace,
				watch.Secret,
				kube.ObjectKeyFromApiObject(opsManager))
		}
	}
}

func (r *OpsManagerReconciler) watchCaReferencedByKmip(opsManager *omv1.MongoDBOpsManager) {
	if !opsManager.Spec.IsKmipEnabled() {
		return
	}

	r.resourceWatcher.AddWatchedResourceIfNotAdded(
		opsManager.Spec.Backup.Encryption.Kmip.Server.CA,
		opsManager.Namespace,
		watch.ConfigMap,
		kube.ObjectKeyFromApiObject(opsManager))
}

func (r *OpsManagerReconciler) watchMongoDBResourcesReferencedByBackup(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) {
	if !opsManager.Spec.Backup.Enabled {
		return
	}

	// watch mongodb resources for oplog
	oplogs := opsManager.Spec.Backup.OplogStoreConfigs
	for _, oplogConfig := range oplogs {
		r.resourceWatcher.AddWatchedResourceIfNotAdded(
			oplogConfig.MongoDBResourceRef.Name,
			opsManager.Namespace,
			watch.MongoDB,
			kube.ObjectKeyFromApiObject(opsManager),
		)
	}

	// watch mongodb resources for block stores
	blockstores := opsManager.Spec.Backup.BlockStoreConfigs
	for _, blockStoreConfig := range blockstores {
		r.resourceWatcher.AddWatchedResourceIfNotAdded(
			blockStoreConfig.MongoDBResourceRef.Name,
			opsManager.Namespace,
			watch.MongoDB,
			kube.ObjectKeyFromApiObject(opsManager),
		)
	}

	// watch mongodb resources for s3 stores
	s3Stores := opsManager.Spec.Backup.S3Configs
	for _, s3StoreConfig := range s3Stores {
		// If S3StoreConfig doesn't have mongodb resource reference, skip it (appdb will be used)
		if s3StoreConfig.MongoDBResourceRef != nil {
			r.resourceWatcher.AddWatchedResourceIfNotAdded(
				s3StoreConfig.MongoDBResourceRef.Name,
				opsManager.Namespace,
				watch.MongoDB,
				kube.ObjectKeyFromApiObject(opsManager),
			)
		}
	}

	r.watchMongoDBResourcesReferencedByKmip(ctx, opsManager, log)
	r.watchCaReferencedByKmip(opsManager)
}

// buildMongoConnectionUrl returns a connection URL to the appdb.
//
// Note, that it overrides the default authMechanism (which internally depends
// on the mongodb version).
func buildMongoConnectionUrl(opsManager *omv1.MongoDBOpsManager, password string, multiClusterHostnames []string) string {
	connectionString := opsManager.Spec.AppDB.BuildConnectionURL(
		util.OpsManagerMongoDBUserName,
		password,
		connectionstring.SchemeMongoDB,
		map[string]string{"authMechanism": "SCRAM-SHA-256"},
		multiClusterHostnames)

	return connectionString
}

func setConfigProperty(opsManager *omv1.MongoDBOpsManager, key, value string, log *zap.SugaredLogger) {
	if opsManager.AddConfigIfDoesntExist(key, value) {
		if key == util.MmsMongoUri {
			log.Debugw("Configured property", key, util.RedactMongoURI(value))
		} else {
			log.Debugw("Configured property", key, value)
		}
	}
}

func (r *OpsManagerReconciler) ensureGenKeyInOperatorCluster(ctx context.Context, om *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (map[string][]byte, error) {
	objectKey := kube.ObjectKey(om.Namespace, om.Name+"-gen-key")
	var opsManagerSecretPath string
	if r.VaultClient != nil {
		opsManagerSecretPath = r.VaultClient.OpsManagerSecretPath()
	}
	genKeySecretMap, err := r.ReadBinarySecret(ctx, objectKey, opsManagerSecretPath)
	if err == nil {
		return genKeySecretMap, nil
	}

	if secrets.SecretNotExist(err) {
		// todo if the key is not found but the AppDB is initialized - OM will fail to start as preflight
		// check will complain that keys are different - we need to validate against this here

		// the length must be equal to 'EncryptionUtils.DES3_KEY_LENGTH' (24) from mms
		token := make([]byte, 24)
		_, err := rand.Read(token)
		if err != nil {
			return nil, err
		}
		keyMap := map[string][]byte{"gen.key": token}

		log.Infof("Creating secret %s", objectKey)

		genKeySecret := secret.Builder().
			SetName(objectKey.Name).
			SetNamespace(objectKey.Namespace).
			SetLabels(map[string]string{}).
			SetByteData(keyMap).
			Build()

		return keyMap, r.PutBinarySecret(ctx, genKeySecret, opsManagerSecretPath)
	}

	return nil, xerrors.Errorf("error reading secret %v: %w", objectKey, err)
}

func (r *OpsManagerReconciler) replicateGenKeyInMemberClusters(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper, secretMap map[string][]byte) error {
	if !reconcileHelper.opsManager.Spec.IsMultiCluster() {
		return nil
	}
	objectKey := kube.ObjectKey(reconcileHelper.opsManager.Namespace, reconcileHelper.opsManager.Name+"-gen-key")
	var opsManagerSecretPath string
	if r.VaultClient != nil {
		opsManagerSecretPath = r.VaultClient.OpsManagerSecretPath()
	}

	genKeySecret := secret.Builder().
		SetName(objectKey.Name).
		SetNamespace(objectKey.Namespace).
		SetLabels(map[string]string{}).
		SetByteData(secretMap).
		Build()

	for _, memberCluster := range reconcileHelper.getHealthyMemberClusters() {
		if err := memberCluster.SecretClient.PutBinarySecret(ctx, genKeySecret, opsManagerSecretPath); err != nil {
			return xerrors.Errorf("error replicating %v secret to cluster %s: %w", objectKey, memberCluster.Name, err)
		}
	}

	return nil
}

func (r *OpsManagerReconciler) replicateQueryableBackupTLSSecretInMemberClusters(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper) error {
	if !reconcileHelper.opsManager.Spec.IsMultiCluster() {
		return nil
	}

	if reconcileHelper.opsManager.Spec.Backup == nil || reconcileHelper.opsManager.Spec.Backup.QueryableBackupSecretRef.Name == "" {
		return nil
	}
	return r.replicateSecretInMemberClusters(ctx, reconcileHelper, reconcileHelper.opsManager.Namespace, reconcileHelper.opsManager.Spec.Backup.QueryableBackupSecretRef.Name)
}

func (r *OpsManagerReconciler) replicateSecretInMemberClusters(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper, namespace string, secretName string) error {
	objectKey := kube.ObjectKey(namespace, secretName)
	var opsManagerSecretPath string
	if r.VaultClient != nil {
		opsManagerSecretPath = r.VaultClient.OpsManagerSecretPath()
	}
	secretMap, err := r.ReadSecret(ctx, objectKey, opsManagerSecretPath)
	if err != nil {
		return xerrors.Errorf("failed to read secret %s: %w", secretName, err)
	}

	newSecret := secret.Builder().
		SetName(objectKey.Name).
		SetNamespace(objectKey.Namespace).
		SetLabels(map[string]string{}).
		SetStringMapToData(secretMap).
		Build()

	for _, memberCluster := range reconcileHelper.getHealthyMemberClusters() {
		if err := memberCluster.SecretClient.PutSecretIfChanged(ctx, newSecret, opsManagerSecretPath); err != nil {
			return xerrors.Errorf("error replicating secret %v to cluster %s: %w", objectKey, memberCluster.Name, err)
		}
	}

	return nil
}

func (r *OpsManagerReconciler) replicateTLSCAInMemberClusters(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper) error {
	if !reconcileHelper.opsManager.Spec.IsMultiCluster() || reconcileHelper.opsManager.Spec.GetOpsManagerCA() == "" {
		return nil
	}

	return r.replicateConfigMapInMemberClusters(ctx, reconcileHelper, reconcileHelper.opsManager.Namespace, reconcileHelper.opsManager.Spec.GetOpsManagerCA())
}

func (r *OpsManagerReconciler) replicateLogBackInMemberClusters(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper) error {
	if !reconcileHelper.opsManager.Spec.IsMultiCluster() || reconcileHelper.opsManager.Spec.Logging == nil {
		return nil
	}

	if reconcileHelper.opsManager.Spec.Logging.LogBackRef != nil {
		if err := r.replicateConfigMapInMemberClusters(ctx, reconcileHelper, reconcileHelper.opsManager.Namespace, reconcileHelper.opsManager.Spec.Logging.LogBackRef.Name); err != nil {
			return err
		}
	}
	if reconcileHelper.opsManager.Spec.Logging.LogBackAccessRef != nil {
		if err := r.replicateConfigMapInMemberClusters(ctx, reconcileHelper, reconcileHelper.opsManager.Namespace, reconcileHelper.opsManager.Spec.Logging.LogBackAccessRef.Name); err != nil {
			return err
		}
	}

	if !reconcileHelper.opsManager.Spec.Backup.Enabled {
		return nil
	}

	if reconcileHelper.opsManager.Spec.Backup.Logging.LogBackRef != nil {
		if err := r.replicateConfigMapInMemberClusters(ctx, reconcileHelper, reconcileHelper.opsManager.Namespace, reconcileHelper.opsManager.Spec.Backup.Logging.LogBackRef.Name); err != nil {
			return err
		}
	}
	if reconcileHelper.opsManager.Spec.Backup.Logging.LogBackAccessRef != nil {
		if err := r.replicateConfigMapInMemberClusters(ctx, reconcileHelper, reconcileHelper.opsManager.Namespace, reconcileHelper.opsManager.Spec.Backup.Logging.LogBackAccessRef.Name); err != nil {
			return err
		}
	}
	return nil
}

func (r *OpsManagerReconciler) replicateAppDBTLSCAInMemberClusters(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper) error {
	if !reconcileHelper.opsManager.Spec.IsMultiCluster() || reconcileHelper.opsManager.Spec.GetAppDbCA() == "" {
		return nil
	}

	return r.replicateConfigMapInMemberClusters(ctx, reconcileHelper, reconcileHelper.opsManager.Namespace, reconcileHelper.opsManager.Spec.GetAppDbCA())
}

func (r *OpsManagerReconciler) replicateKMIPCAInMemberClusters(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper) error {
	if !reconcileHelper.opsManager.Spec.IsKmipEnabled() {
		return nil
	}

	return r.replicateConfigMapInMemberClusters(ctx, reconcileHelper, reconcileHelper.opsManager.Namespace, reconcileHelper.opsManager.Spec.Backup.Encryption.Kmip.Server.CA)
}

func (r *OpsManagerReconciler) replicateConfigMapInMemberClusters(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper, namespace string, configMapName string) error {
	if !reconcileHelper.opsManager.Spec.IsMultiCluster() || configMapName == "" {
		return nil
	}

	configMapNSName := types.NamespacedName{Name: configMapName, Namespace: namespace}
	caConfigMapData, err := configmap.ReadData(ctx, r.client, configMapNSName)
	if err != nil {
		return xerrors.Errorf("failed to read config map %+v from central cluster: %w", configMapNSName, err)
	}

	caConfigMap := configmap.Builder().
		SetName(configMapNSName.Name).
		SetNamespace(configMapNSName.Namespace).
		SetData(caConfigMapData).
		Build()

	for _, memberCluster := range reconcileHelper.getHealthyMemberClusters() {
		if err := configmap.CreateOrUpdate(ctx, memberCluster.Client, caConfigMap); err != nil && !apiErrors.IsAlreadyExists(err) {
			return xerrors.Errorf("failed to create or update config map %+v in cluster %s: %w", configMapNSName, memberCluster.Name, err)
		}
	}

	return nil
}

func (r *OpsManagerReconciler) getOpsManagerAPIKeySecretName(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (string, workflow.Status) {
	var operatorVaultSecretPath string
	if r.VaultClient != nil {
		operatorVaultSecretPath = r.VaultClient.OperatorSecretPath()
	}
	APISecretName, err := opsManager.APIKeySecretName(ctx, r.SecretClient, operatorVaultSecretPath)
	if err != nil {
		return "", workflow.Failed(xerrors.Errorf("failed to get ops-manager API key secret name: %w", err)).WithRetry(10)
	}
	return APISecretName, workflow.OK()
}

func detailedAPIErrorMsg(adminKeySecretName types.NamespacedName) string {
	return fmt.Sprintf("This is a fatal error, as the"+
		" Operator requires public API key for the admin user to exist. Please create the GLOBAL_ADMIN user in "+
		"Ops Manager manually and create a secret '%s' with fields '%s' and '%s'", adminKeySecretName, util.OmPublicApiKey,
		util.OmPrivateKey)
}

// prepareOpsManager ensures the admin user is created and the admin public key exists. It returns the instance of
// api.OpsManagerAdmin to perform future Ops Manager configuration
// Note the exception handling logic - if the controller fails to save the public API key secret - it cannot fix this
// manually (the first OM user can be created only once) - so the resource goes to Failed state and shows the message
// asking the user to fix this manually.
// Theoretically, the Operator could remove the appdb StatefulSet (as the OM must be empty without any user data) and
// allow the db to get recreated, but this is a quite radical operation.
func (r *OpsManagerReconciler) prepareOpsManager(ctx context.Context, opsManager *omv1.MongoDBOpsManager, centralURL string, log *zap.SugaredLogger) (workflow.Status, api.OpsManagerAdmin) {
	// We won't support cross-namespace secrets until CLOUDP-46636 is resolved
	adminObjectKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AdminSecret)

	var operatorVaultPath string
	if r.VaultClient != nil {
		operatorVaultPath = r.VaultClient.OperatorSecretPath()
	}

	// 1. Read the admin secret
	userData, err := r.ReadSecret(ctx, adminObjectKey, operatorVaultPath)

	if secrets.SecretNotExist(err) {
		// This requires user actions - let's wait a bit longer than 10 seconds
		return workflow.Failed(xerrors.Errorf("the secret %s doesn't exist - you need to create it to finish Ops Manager initialization", adminObjectKey)).WithRetry(60), nil
	} else if err != nil {
		return workflow.Failed(err), nil
	}

	newUser, err := newUserFromSecret(userData)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("failed to read user data from the secret %s: %w", adminObjectKey, err)), nil
	}

	var ca *string
	if opsManager.IsTLSEnabled() {
		log.Debug("TLS is enabled, creating the first user with the mms-ca.crt")
		opsManagerCA := opsManager.Spec.GetOpsManagerCA()
		cm, err := r.client.GetConfigMap(ctx, kube.ObjectKey(opsManager.Namespace, opsManagerCA))
		if err != nil {
			return workflow.Failed(xerrors.Errorf("failed to retrieve om ca certificate to create the initial user: %w", err)).WithRetry(30), nil
		}
		ca = ptr.To(cm.Data["mms-ca.crt"])
	}

	APISecretName, status := r.getOpsManagerAPIKeySecretName(ctx, opsManager)
	if !status.IsOK() {
		return status, nil
	}
	adminKeySecretName := kube.ObjectKey(operatorNamespace(), APISecretName)
	// 2. Create a user in Ops Manager if necessary. Note, that we don't send the request if the API key secret exists.
	// This is because of the weird Ops Manager /unauth endpoint logic: it allows to create any number of users though only
	// the first one will have GLOBAL_ADMIN permission. So we should avoid the situation when the admin changes the
	// user secret and reconciles OM resource and the new user (non admin one) is created overriding the previous API secret
	_, err = r.ReadSecret(ctx, adminKeySecretName, operatorVaultPath)
	if secrets.SecretNotExist(err) {
		apiKey, err := r.omInitializer.TryCreateUser(centralURL, opsManager.Spec.Version, newUser, ca)
		if err != nil {
			// Will wait more than usual (10 seconds) as most of all the problem needs to get fixed by the user
			// by modifying the credentials secret
			return workflow.Failed(xerrors.Errorf("failed to create an admin user in Ops Manager: %w", err)).WithRetry(30), nil
		}

		// Recreate an admin key secret in the Operator namespace if the user was created
		if apiKey.PublicKey != "" {
			log.Infof("Created an admin user %s with GLOBAL_ADMIN role", newUser.Username)

			// The structure matches the structure of a credentials secret used by normal mongodb resources
			secretData := map[string]string{util.OmPublicApiKey: apiKey.PublicKey, util.OmPrivateKey: apiKey.PrivateKey}

			if err = r.client.DeleteSecret(ctx, adminKeySecretName); err != nil && !secrets.SecretNotExist(err) {
				// TODO our desired behavior is not to fail but just append the warning to the status (CLOUDP-51340)
				return workflow.Failed(xerrors.Errorf("failed to replace a secret for admin public api key. %s. The error : %w",
					detailedAPIErrorMsg(adminKeySecretName), err)).WithRetry(300), nil
			}

			adminSecret := secret.Builder().
				SetNamespace(adminKeySecretName.Namespace).
				SetName(adminKeySecretName.Name).
				SetStringMapToData(secretData).
				SetLabels(map[string]string{}).Build()

			if err := r.PutSecret(ctx, adminSecret, operatorVaultPath); err != nil {
				return workflow.Failed(xerrors.Errorf("failed to create a secret for admin public api key. %s. The error : %w",
					detailedAPIErrorMsg(adminKeySecretName), err)).WithRetry(30), nil
			}
			log.Infof("Created a secret for admin public api key %s", adminKeySecretName)
		} else {
			log.Debug("Ops Manager did not return a valid User object.")
		}
	}

	// 3. Final validation of admin secret. We must ensure it's refreshed by the informers as ReadCredentials
	// is going to fail otherwise.
	readAdminKeySecretFunc := func() (string, bool) {
		if _, err = r.ReadSecret(ctx, adminKeySecretName, operatorVaultPath); err != nil {
			return fmt.Sprintf("%v", err), false
		}
		return "", true
	}
	if found, msg := util.DoAndRetry(readAdminKeySecretFunc, log, 10, 5); !found {
		return workflow.Failed(xerrors.Errorf("admin API key secret for Ops Manager doesn't exist - was it removed accidentally? %s. The error: %s",
			detailedAPIErrorMsg(adminKeySecretName), msg)).WithRetry(30), nil
	}

	// Ops Manager api key Secret has the same structure as the MongoDB credentials secret
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, r.SecretClient, operatorVaultPath)
	if err != nil {
		return workflow.Failed(err), nil
	}

	cred, err := project.ReadCredentials(ctx, r.SecretClient, kube.ObjectKey(operatorNamespace(), APIKeySecretName), log)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("failed to locate the api key secret. The error : %w", err)), nil
	}

	admin := r.omAdminProvider(centralURL, cred.PublicAPIKey, cred.PrivateAPIKey, ca)
	return workflow.OK(), admin
}

// prepareBackupInOpsManager makes the changes to backup admin configuration based on the Ops Manager spec
func (r *OpsManagerReconciler) prepareBackupInOpsManager(ctx context.Context, reconcileHelper *OpsManagerReconcilerHelper, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, appDBConnectionString string, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	// 1. Enabling Daemon Config if necessary
	backupFQDNs := reconcileHelper.BackupDaemonHeadlessFQDNs()
	for _, fqdn := range backupFQDNs {
		dc, err := omAdmin.ReadDaemonConfig(fqdn.hostname, util.PvcMountPathHeadDb)
		if apierror.NewNonNil(err).ErrorBackupDaemonConfigIsNotFound() {
			log.Infow("Backup Daemon is not configured, enabling it", "hostname", fqdn.hostname, "headDB", util.PvcMountPathHeadDb)

			err = omAdmin.CreateDaemonConfig(fqdn.hostname, util.PvcMountPathHeadDb, opsManager.GetMemberClusterBackupAssignmentLabels(fqdn.memberClusterName))
			if apierror.NewNonNil(err).ErrorBackupDaemonConfigIsNotFound() {
				// Unfortunately by this time backup daemon may not have been started yet, and we don't have proper
				// mechanism to ensure this using readiness probe, so we just retry
				return workflow.Pending("BackupDaemon hasn't started yet")
			} else if err != nil {
				return workflow.Failed(xerrors.New(err.Error()))
			}
		} else if err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		} else {
			// The Assignment Labels are the only thing that can change at the moment.
			// If we add new features for controlling the Backup Daemons, we may want
			// to compare the whole backup.DaemonConfig objects.
			assignmentLabels := opsManager.GetMemberClusterBackupAssignmentLabels(fqdn.memberClusterName)
			if !reflect.DeepEqual(assignmentLabels, dc.Labels) {
				dc.Labels = assignmentLabels
				err = omAdmin.UpdateDaemonConfig(dc)
				if err != nil {
					return workflow.Failed(xerrors.New(err.Error()))
				}
			}
		}
	}

	// 2. Oplog store configs
	status := r.ensureOplogStoresInOpsManager(ctx, opsManager, omAdmin, log)

	// 3. S3 Oplog Configs
	status = status.Merge(r.ensureS3OplogStoresInOpsManager(ctx, opsManager, omAdmin, appDBConnectionString, log))

	// 4. S3 Configs
	status = status.Merge(r.ensureS3ConfigurationInOpsManager(ctx, opsManager, omAdmin, appDBConnectionString, log))

	// 5. Block store configs
	status = status.Merge(r.ensureBlockStoresInOpsManager(ctx, opsManager, omAdmin, log))

	// 6. FileSystem store configs
	status = status.Merge(r.ensureFileSystemStoreConfigurationInOpsManager(opsManager, omAdmin))
	if len(opsManager.Spec.Backup.S3Configs) == 0 && len(opsManager.Spec.Backup.BlockStoreConfigs) == 0 && len(opsManager.Spec.Backup.FileSystemStoreConfigs) == 0 {
		return status.Merge(workflow.Invalid("Either S3 or Blockstore or FileSystem Snapshot configuration is required for backup").WithTargetPhase(mdbstatus.PhasePending))
	}

	return status
}

// ensureOplogStoresInOpsManager aligns the oplog stores in Ops Manager with the Operator state. So it adds the new configs
// and removes the non-existing ones. Note that there's no update operation as so far the Operator manages only one field
// 'path'. This will allow users to make any additional changes to the file system stores using Ops Manager UI and the
// Operator won't override them
func (r *OpsManagerReconciler) ensureOplogStoresInOpsManager(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OplogStoreAdmin, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerOplogConfigs, err := omAdmin.ReadOplogStoreConfigs()
	if err != nil {
		return workflow.Failed(xerrors.New(err.Error()))
	}

	// Creating new configs
	operatorOplogConfigs := opsManager.Spec.Backup.OplogStoreConfigs
	configsToCreate := identifiable.SetDifferenceGeneric(operatorOplogConfigs, opsManagerOplogConfigs)
	for _, v := range configsToCreate {
		omConfig, status := r.buildOMDatastoreConfig(ctx, opsManager, v.(omv1.DataStoreConfig))
		if !status.IsOK() {
			return status
		}
		log.Debugw("Creating Oplog Store in Ops Manager", "config", omConfig)
		if err = omAdmin.CreateOplogStoreConfig(omConfig); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := identifiable.SetIntersectionGeneric(opsManagerOplogConfigs, operatorOplogConfigs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.DataStoreConfig)
		operatorConfig := v[1].(omv1.DataStoreConfig)
		operatorView, status := r.buildOMDatastoreConfig(ctx, opsManager, operatorConfig)
		if !status.IsOK() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Debugw("Updating Oplog Store in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateOplogStoreConfig(configToUpdate); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	// Removing non-existing configs
	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerOplogConfigs, opsManager.Spec.Backup.OplogStoreConfigs)
	for _, v := range configsToRemove {
		log.Debugf("Removing Oplog Store %s from Ops Manager", v.Identifier())
		if err = omAdmin.DeleteOplogStoreConfig(v.Identifier().(string)); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	operatorS3OplogConfigs := opsManager.Spec.Backup.S3OplogStoreConfigs
	if len(operatorOplogConfigs) == 0 && len(operatorS3OplogConfigs) == 0 {
		return workflow.Invalid("Oplog Store configuration is required for backup").WithTargetPhase(mdbstatus.PhasePending)
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) ensureS3OplogStoresInOpsManager(ctx context.Context, opsManager *omv1.MongoDBOpsManager, s3OplogAdmin api.S3OplogStoreAdmin, appDBConnectionString string, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerS3OpLogConfigs, err := s3OplogAdmin.ReadS3OplogStoreConfigs()
	if err != nil {
		return workflow.Failed(xerrors.New(err.Error()))
	}

	// Creating new configs
	s3OperatorOplogConfigs := opsManager.Spec.Backup.S3OplogStoreConfigs
	configsToCreate := identifiable.SetDifferenceGeneric(s3OperatorOplogConfigs, opsManagerS3OpLogConfigs)
	for _, v := range configsToCreate {
		omConfig, status := r.buildOMS3Config(ctx, opsManager, v.(omv1.S3Config), true, appDBConnectionString)
		if !status.IsOK() {
			return status
		}
		log.Infow("Creating S3 Oplog Store in Ops Manager", "config", omConfig)
		if err = s3OplogAdmin.CreateS3OplogStoreConfig(omConfig); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := identifiable.SetIntersectionGeneric(opsManagerS3OpLogConfigs, s3OperatorOplogConfigs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.S3Config)
		operatorConfig := v[1].(omv1.S3Config)
		operatorView, status := r.buildOMS3Config(ctx, opsManager, operatorConfig, true, appDBConnectionString)
		if !status.IsOK() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Infow("Updating S3 Oplog Store in Ops Manager", "config", configToUpdate)
		if err = s3OplogAdmin.UpdateS3OplogConfig(configToUpdate); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	// Removing non-existing configs
	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerS3OpLogConfigs, opsManager.Spec.Backup.S3OplogStoreConfigs)
	for _, v := range configsToRemove {
		log.Infof("Removing Oplog Store %s from Ops Manager", v.Identifier())
		if err = s3OplogAdmin.DeleteS3OplogStoreConfig(v.Identifier().(string)); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	operatorOplogConfigs := opsManager.Spec.Backup.OplogStoreConfigs
	if len(operatorOplogConfigs) == 0 && len(s3OperatorOplogConfigs) == 0 {
		return workflow.Invalid("Oplog Store configuration is required for backup").WithTargetPhase(mdbstatus.PhasePending)
	}
	return workflow.OK()
}

// ensureBlockStoresInOpsManager aligns the blockStore configs in Ops Manager with the Operator state. So it adds the new configs
// and removes the non-existing ones. Note that there's no update operation as so far the Operator manages only one field
// 'path'. This will allow users to make any additional changes to the file system stores using Ops Manager UI and the
// Operator won't override them
func (r *OpsManagerReconciler) ensureBlockStoresInOpsManager(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.BlockStoreAdmin, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerBlockStoreConfigs, err := omAdmin.ReadBlockStoreConfigs()
	if err != nil {
		return workflow.Failed(xerrors.New(err.Error()))
	}

	// Creating new configs
	operatorBlockStoreConfigs := opsManager.Spec.Backup.BlockStoreConfigs
	configsToCreate := identifiable.SetDifferenceGeneric(operatorBlockStoreConfigs, opsManagerBlockStoreConfigs)
	for _, v := range configsToCreate {
		omConfig, status := r.buildOMDatastoreConfig(ctx, opsManager, v.(omv1.DataStoreConfig))
		if !status.IsOK() {
			return status
		}
		log.Debugw("Creating Block Store in Ops Manager", "config", omConfig)
		if err = omAdmin.CreateBlockStoreConfig(omConfig); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := identifiable.SetIntersectionGeneric(opsManagerBlockStoreConfigs, operatorBlockStoreConfigs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.DataStoreConfig)
		operatorConfig := v[1].(omv1.DataStoreConfig)
		operatorView, status := r.buildOMDatastoreConfig(ctx, opsManager, operatorConfig)
		if !status.IsOK() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Debugw("Updating Block Store in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateBlockStoreConfig(configToUpdate); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	// Removing non-existing configs
	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerBlockStoreConfigs, opsManager.Spec.Backup.BlockStoreConfigs)
	for _, v := range configsToRemove {
		log.Debugf("Removing Block Store %s from Ops Manager", v.Identifier())
		if err = omAdmin.DeleteBlockStoreConfig(v.Identifier().(string)); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) ensureS3ConfigurationInOpsManager(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.S3StoreBlockStoreAdmin, appDBConnectionString string, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerS3Configs, err := omAdmin.ReadS3Configs()
	if err != nil {
		return workflow.Failed(xerrors.New(err.Error()))
	}

	operatorS3Configs := opsManager.Spec.Backup.S3Configs
	configsToCreate := identifiable.SetDifferenceGeneric(operatorS3Configs, opsManagerS3Configs)
	for _, config := range configsToCreate {
		omConfig, status := r.buildOMS3Config(ctx, opsManager, config.(omv1.S3Config), false, appDBConnectionString)
		if !status.IsOK() {
			return status
		}

		log.Infow("Creating S3Config in Ops Manager", "config", omConfig)
		if err := omAdmin.CreateS3Config(omConfig); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := identifiable.SetIntersectionGeneric(opsManagerS3Configs, operatorS3Configs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.S3Config)
		operatorConfig := v[1].(omv1.S3Config)
		operatorView, status := r.buildOMS3Config(ctx, opsManager, operatorConfig, false, appDBConnectionString)
		if !status.IsOK() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Infow("Updating S3Config in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateS3Config(configToUpdate); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerS3Configs, operatorS3Configs)
	for _, config := range configsToRemove {
		log.Infof("Removing S3Config %s from Ops Manager", config.Identifier())
		if err := omAdmin.DeleteS3Config(config.Identifier().(string)); err != nil {
			return workflow.Failed(xerrors.New(err.Error()))
		}
	}

	return workflow.OK()
}

// readS3Credentials reads the access and secret keys from the awsCredentials secret specified
// in the resource
func (r *OpsManagerReconciler) readS3Credentials(ctx context.Context, s3SecretName, namespace string) (*backup.S3Credentials, error) {
	var operatorSecretPath string
	if r.VaultClient != nil {
		operatorSecretPath = r.VaultClient.OperatorSecretPath()
	}

	s3SecretData, err := r.ReadSecret(ctx, kube.ObjectKey(namespace, s3SecretName), operatorSecretPath)
	if err != nil {
		return nil, xerrors.New(err.Error())
	}

	s3Creds := &backup.S3Credentials{}
	if accessKey, ok := s3SecretData[util.S3AccessKey]; !ok {
		return nil, xerrors.Errorf("key %s was not present in the secret %s", util.S3AccessKey, s3SecretName)
	} else {
		s3Creds.AccessKey = accessKey
	}

	if secretKey, ok := s3SecretData[util.S3SecretKey]; !ok {
		return nil, xerrors.Errorf("key %s was not present in the secret %s", util.S3SecretKey, s3SecretName)
	} else {
		s3Creds.SecretKey = secretKey
	}

	return s3Creds, nil
}

// ensureFileSystemStoreConfigurationInOpsManage makes sure that the FileSystem snapshot stores specified in the
// MongoDB CR are configured correctly in OpsManager.
func (r *OpsManagerReconciler) ensureFileSystemStoreConfigurationInOpsManager(opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin) workflow.Status {
	opsManagerFSStoreConfigs, err := omAdmin.ReadFileSystemStoreConfigs()
	if err != nil {
		return workflow.Failed(xerrors.New(err.Error()))
	}

	fsStoreNames := make(map[string]struct{})
	for _, e := range opsManager.Spec.Backup.FileSystemStoreConfigs {
		fsStoreNames[e.Name] = struct{}{}
	}
	// count the number of FS snapshots configured in OM and match them with the one in CR.
	countFS := 0

	for _, e := range opsManagerFSStoreConfigs {
		if _, ok := fsStoreNames[e.Id]; ok {
			countFS++
		}
	}

	if countFS != len(opsManager.Spec.Backup.FileSystemStoreConfigs) {
		return workflow.Failed(xerrors.Errorf("Not all fileSystem snapshots have been configured in OM."))
	}
	return workflow.OK()
}

// shouldUseAppDb accepts an S3Config and returns true if the AppDB should be used
// for this S3 configuration. Otherwise, a MongoDB resource is configured for use.
func shouldUseAppDb(config omv1.S3Config) bool {
	return config.MongoDBResourceRef == nil
}

// buildAppDbOMS3Config creates a backup.S3Config which is configured to use The AppDb.
func (r *OpsManagerReconciler) buildAppDbOMS3Config(ctx context.Context, om *omv1.MongoDBOpsManager, config omv1.S3Config, isOpLog bool, appDBConnectionString string) (backup.S3Config, workflow.Status) {
	var s3Creds *backup.S3Credentials

	if !config.IRSAEnabled {
		var err error
		s3Creds, err = r.readS3Credentials(ctx, config.S3SecretRef.Name, om.Namespace)
		if err != nil {
			return backup.S3Config{}, workflow.Failed(xerrors.New(err.Error()))
		}
	}

	bucket := backup.S3Bucket{
		Endpoint: config.S3BucketEndpoint,
		Name:     config.S3BucketName,
	}

	customCAOpts, err := r.readCustomCAFilePathsAndContents(ctx, om, isOpLog)
	if err != nil {
		return backup.S3Config{}, workflow.Failed(xerrors.New(err.Error()))
	}

	return backup.NewS3Config(om, config, appDBConnectionString, customCAOpts, bucket, s3Creds), workflow.OK()
}

// buildMongoDbOMS3Config creates a backup.S3Config which is configured to use a referenced
// MongoDB resource.
func (r *OpsManagerReconciler) buildMongoDbOMS3Config(ctx context.Context, opsManager *omv1.MongoDBOpsManager, config omv1.S3Config, isOpLog bool) (backup.S3Config, workflow.Status) {
	mongodb, status := r.getMongoDbForS3Config(ctx, opsManager, config)
	if !status.IsOK() {
		return backup.S3Config{}, status
	}

	if status := validateS3Config(mongodb.GetAuthenticationModes(), mongodb.GetResourceName(), config); !status.IsOK() {
		return backup.S3Config{}, status
	}

	userName, password, status := r.getS3MongoDbUserNameAndPassword(ctx, mongodb.GetAuthenticationModes(), opsManager.Namespace, config)
	if !status.IsOK() {
		return backup.S3Config{}, status
	}

	var s3Creds *backup.S3Credentials
	var err error

	if !config.IRSAEnabled {
		s3Creds, err = r.readS3Credentials(ctx, config.S3SecretRef.Name, opsManager.Namespace)
		if err != nil {
			return backup.S3Config{}, workflow.Failed(err)
		}
	}

	uri := mongodb.BuildConnectionString(userName, password, connectionstring.SchemeMongoDB, map[string]string{})

	bucket := backup.S3Bucket{
		Endpoint: config.S3BucketEndpoint,
		Name:     config.S3BucketName,
	}

	customCAOpts, err := r.readCustomCAFilePathsAndContents(ctx, opsManager, isOpLog)
	if err != nil {
		return backup.S3Config{}, workflow.Failed(err)
	}

	return backup.NewS3Config(opsManager, config, uri, customCAOpts, bucket, s3Creds), workflow.OK()
}

// readCustomCAFilePathsAndContents returns the filepath and contents of the custom CA which is used to configure
// the S3Store.
func (r *OpsManagerReconciler) readCustomCAFilePathsAndContents(ctx context.Context, opsManager *omv1.MongoDBOpsManager, isOpLog bool) ([]backup.S3CustomCertificate, error) {
	var customCertificates []backup.S3CustomCertificate
	var err error

	if isOpLog {
		customCertificates, err = getCAs(ctx, opsManager.Spec.Backup.S3OplogStoreConfigs, opsManager.Namespace, r.client)
	} else {
		customCertificates, err = getCAs(ctx, opsManager.Spec.Backup.S3Configs, opsManager.Namespace, r.client)
	}

	if err != nil {
		return customCertificates, err
	}

	if opsManager.Spec.GetAppDbCA() != "" {
		cmContents, err := configmap.ReadKey(ctx, r.client, "ca-pem", kube.ObjectKey(opsManager.Namespace, opsManager.Spec.GetAppDbCA()))
		if err != nil {
			return []backup.S3CustomCertificate{}, xerrors.New(err.Error())
		}
		customCertificates = append(customCertificates, backup.S3CustomCertificate{
			Filename:   omv1.GetAppDBCaPemPath(),
			CertString: cmContents,
		})
	}

	return customCertificates, nil
}

func getCAs(ctx context.Context, s3Config []omv1.S3Config, ns string, client secret.Getter) ([]backup.S3CustomCertificate, error) {
	var certificates []backup.S3CustomCertificate
	for _, config := range s3Config {
		for _, backupCert := range config.CustomCertificateSecretRefs {
			if backupCert.Name != "" {
				aliasName := backupCert.Name + "/" + backupCert.Key
				if cmContents, err := secret.ReadKey(ctx, client, backupCert.Key, kube.ObjectKey(ns, backupCert.Name)); err != nil {
					return []backup.S3CustomCertificate{}, xerrors.New(err.Error())
				} else {
					certificates = append(certificates, backup.S3CustomCertificate{
						Filename:   aliasName,
						CertString: cmContents,
					})
				}
			}
		}
	}
	return certificates, nil
}

// buildOMS3Config builds the OM API S3 config from the Operator OM CR configuration. This involves some logic to
// get the mongo URI, which points to either the external resource or to the AppDB
func (r *OpsManagerReconciler) buildOMS3Config(ctx context.Context, opsManager *omv1.MongoDBOpsManager, config omv1.S3Config, isOpLog bool, appDBConnectionString string) (backup.S3Config, workflow.Status) {
	if shouldUseAppDb(config) {
		return r.buildAppDbOMS3Config(ctx, opsManager, config, isOpLog, appDBConnectionString)
	}
	return r.buildMongoDbOMS3Config(ctx, opsManager, config, isOpLog)
}

// getMongoDbForS3Config returns the referenced MongoDB resource which should be used when configuring the backup config.
func (r *OpsManagerReconciler) getMongoDbForS3Config(ctx context.Context, opsManager *omv1.MongoDBOpsManager, config omv1.S3Config) (S3ConfigGetter, workflow.Status) {
	mongodb, mongodbMulti := &mdbv1.MongoDB{}, &mdbmulti.MongoDBMultiCluster{}
	mongodbObjectKey := config.MongodbResourceObjectKey(opsManager)

	err := r.client.Get(ctx, mongodbObjectKey, mongodb)
	if err != nil {
		if secrets.SecretNotExist(err) {

			// try to fetch mongodbMulti if it exists
			err = r.client.Get(ctx, mongodbObjectKey, mongodbMulti)
			if err != nil {
				if secrets.SecretNotExist(err) {
					// Returning pending as the user may create the mongodb resource soon
					return nil, workflow.Pending("The MongoDB object %s doesn't exist", mongodbObjectKey)
				}
				return nil, workflow.Failed(xerrors.New(err.Error()))
			}
			return mongodbMulti, workflow.OK()
		}

		return nil, workflow.Failed(err)
	}

	return mongodb, workflow.OK()
}

// getS3MongoDbUserNameAndPassword returns userName and password if MongoDB resource has scram-sha enabled.
// Note, that we don't worry if the 'mongodbUserRef' is specified but SCRAM-SHA is not enabled - we just ignore the
// user.
func (r *OpsManagerReconciler) getS3MongoDbUserNameAndPassword(ctx context.Context, modes []string, namespace string, config omv1.S3Config) (string, string, workflow.Status) {
	if !stringutil.Contains(modes, util.SCRAM) {
		return "", "", workflow.OK()
	}
	mongodbUser := &user.MongoDBUser{}
	mongodbUserObjectKey := config.MongodbUserObjectKey(namespace)
	err := r.client.Get(ctx, mongodbUserObjectKey, mongodbUser)
	if secrets.SecretNotExist(err) {
		return "", "", workflow.Pending("The MongoDBUser object %s doesn't exist", mongodbUserObjectKey)
	}
	if err != nil {
		return "", "", workflow.Failed(xerrors.Errorf("Failed to fetch the user %s: %w", mongodbUserObjectKey, err))
	}
	userName := mongodbUser.Spec.Username
	password, err := mongodbUser.GetPassword(ctx, r.SecretClient)
	if err != nil {
		return "", "", workflow.Failed(xerrors.Errorf("Failed to read password for the user %s: %w", mongodbUserObjectKey, err))
	}
	return userName, password, workflow.OK()
}

// buildOMDatastoreConfig builds the OM API datastore config based on the Kubernetes OM resource one.
// To do this it may need to read the Mongodb User and its password to build mongodb url correctly
func (r *OpsManagerReconciler) buildOMDatastoreConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, operatorConfig omv1.DataStoreConfig) (backup.DataStoreConfig, workflow.Status) {
	mongodb := &mdbv1.MongoDB{}
	mongodbObjectKey := operatorConfig.MongodbResourceObjectKey(opsManager.Namespace)

	err := r.client.Get(ctx, mongodbObjectKey, mongodb)
	if err != nil {
		if secrets.SecretNotExist(err) {
			// Returning pending as the user may create the mongodb resource soon
			return backup.DataStoreConfig{}, workflow.Pending("The MongoDB object %s doesn't exist", mongodbObjectKey)
		}
		return backup.DataStoreConfig{}, workflow.Failed(xerrors.New(err.Error()))
	}

	status := validateDataStoreConfig(mongodb.Spec.Security.Authentication.GetModes(), mongodb.Name, operatorConfig)
	if !status.IsOK() {
		return backup.DataStoreConfig{}, status
	}

	// If MongoDB resource has scram-sha enabled then we need to read the username and the password.
	// Note, that we don't worry if the 'mongodbUserRef' is specified but SCRAM-SHA is not enabled - we just ignore the
	// user
	var userName, password string
	if stringutil.Contains(mongodb.Spec.Security.Authentication.GetModes(), util.SCRAM) {
		mongodbUser := &user.MongoDBUser{}
		mongodbUserObjectKey := operatorConfig.MongodbUserObjectKey(opsManager.Namespace)
		err := r.client.Get(ctx, mongodbUserObjectKey, mongodbUser)
		if secrets.SecretNotExist(err) {
			return backup.DataStoreConfig{}, workflow.Pending("The MongoDBUser object %s doesn't exist", operatorConfig.MongodbResourceObjectKey(opsManager.Namespace))
		}
		if err != nil {
			return backup.DataStoreConfig{}, workflow.Failed(xerrors.Errorf("Failed to fetch the user %s: %w", operatorConfig.MongodbResourceObjectKey(opsManager.Namespace), err))
		}
		userName = mongodbUser.Spec.Username
		password, err = mongodbUser.GetPassword(ctx, r.SecretClient)
		if err != nil {
			return backup.DataStoreConfig{}, workflow.Failed(xerrors.Errorf("Failed to read password for the user %s: %w", mongodbUserObjectKey, err))
		}
	}

	tls := mongodb.Spec.Security.TLSConfig.Enabled
	mongoUri := mongodb.BuildConnectionString(userName, password, connectionstring.SchemeMongoDB, map[string]string{})
	return backup.NewDataStoreConfig(operatorConfig.Name, mongoUri, tls, operatorConfig.AssignmentLabels), workflow.OK()
}

func validateS3Config(modes []string, mdbName string, s3Config omv1.S3Config) workflow.Status {
	return validateConfig(modes, mdbName, s3Config.MongoDBUserRef, "S3 metadata database")
}

func validateDataStoreConfig(modes []string, mdbName string, dataStoreConfig omv1.DataStoreConfig) workflow.Status {
	return validateConfig(modes, mdbName, dataStoreConfig.MongoDBUserRef, "Oplog/Blockstore databases")
}

func validateConfig(modes []string, mdbName string, userRef *omv1.MongoDBUserRef, description string) workflow.Status {
	// validate
	if !stringutil.Contains(modes, util.SCRAM) &&
		len(modes) > 0 {
		return workflow.Failed(xerrors.Errorf("The only authentication mode supported for the %s is SCRAM-SHA", description))
	}
	if stringutil.Contains(modes, util.SCRAM) &&
		(userRef == nil || userRef.Name == "") {
		return workflow.Failed(xerrors.Errorf("MongoDB resource %s is configured to use SCRAM-SHA authentication mode, the user must be"+
			" specified using 'mongodbUserRef'", mdbName))
	}

	return workflow.OK()
}

func newUserFromSecret(data map[string]string) (api.User, error) {
	// validate
	for _, v := range []string{"Username", "Password", "FirstName", "LastName"} {
		if _, ok := data[v]; !ok {
			return api.User{}, xerrors.Errorf("%s property is missing in the admin secret", v)
		}
	}
	newUser := api.User{
		Username:  data["Username"],
		Password:  data["Password"],
		FirstName: data["FirstName"],
		LastName:  data["LastName"],
	}
	return newUser, nil
}

// OnDelete cleans up Ops Manager related resources on CR removal.
// it's used in MongoDBOpsManagerEventHandler
func (r *OpsManagerReconciler) OnDelete(ctx context.Context, obj interface{}, log *zap.SugaredLogger) {
	opsManager := obj.(*omv1.MongoDBOpsManager)
	helper, err := NewOpsManagerReconcilerHelper(ctx, r, opsManager, r.memberClustersMap, log)
	if err != nil {
		log.Errorf("Error initializing OM reconciler helper: %s", err)
		return
	}

	// r.resourceWatcher.RemoveAllDependentWatchedResources(opsManager.Namespace, kube.ObjectKeyFromApiObject(opsManager))
	for _, memberCluster := range helper.getHealthyMemberClusters() {
		stsName := helper.OpsManagerStatefulSetNameForMemberCluster(memberCluster)
		r.resourceWatcher.RemoveAllDependentWatchedResources(opsManager.Namespace, kube.ObjectKey(opsManager.Namespace, stsName))
	}

	for _, memberCluster := range helper.getHealthyMemberClusters() {
		memberClient := memberCluster.Client
		stsName := helper.OpsManagerStatefulSetNameForMemberCluster(memberCluster)
		err := memberClient.DeleteStatefulSet(ctx, kube.ObjectKey(opsManager.Namespace, stsName))
		if err != nil {
			log.Warnf("Failed to delete statefulset: %s in cluster: %s", stsName, memberCluster.Name)
		}
	}

	for _, memberCluster := range helper.getHealthyMemberClusters() {
		stsName := helper.BackupDaemonStatefulSetNameForMemberCluster(memberCluster)
		r.resourceWatcher.RemoveAllDependentWatchedResources(opsManager.Namespace, kube.ObjectKey(opsManager.Namespace, stsName))
	}

	for _, memberCluster := range helper.getHealthyMemberClusters() {
		memberClient := memberCluster.Client
		stsName := helper.BackupDaemonStatefulSetNameForMemberCluster(memberCluster)
		err := memberClient.DeleteStatefulSet(ctx, kube.ObjectKey(opsManager.Namespace, stsName))
		if err != nil {
			log.Warnf("Failed to delete statefulset: %s in cluster: %s", stsName, memberCluster.Name)
		}
	}

	appDbReconciler, err := r.createNewAppDBReconciler(ctx, opsManager, log)
	if err != nil {
		log.Errorf("Error initializing AppDB reconciler: %s", err)
		return
	}

	// remove AppDB from each of the member clusters(or the same cluster as OM in case of single cluster )
	for _, memberCluster := range appDbReconciler.GetHealthyMemberClusters() {
		// fetch the clusterNum for a given clusterName
		r.resourceWatcher.RemoveAllDependentWatchedResources(opsManager.Namespace, opsManager.AppDBStatefulSetObjectKey(appDbReconciler.getMemberClusterIndex(memberCluster.Name)))
	}

	// delete the AppDB statefulset form each of the member cluster. We need to delete the
	// resource explicitly in case of multi-cluster because we can't set owner reference cross cluster
	for _, memberCluster := range appDbReconciler.GetHealthyMemberClusters() {
		memberClient := memberCluster.Client
		stsNamespacedName := opsManager.AppDBStatefulSetObjectKey(appDbReconciler.getMemberClusterIndex(memberCluster.Name))

		err := memberClient.DeleteStatefulSet(ctx, stsNamespacedName)
		if err != nil {
			log.Warnf("Failed to delete statefulset: %s in cluster: %s", stsNamespacedName, memberCluster.Name)
		}
	}
	log.Info("Cleaned up Ops Manager related resources.")
}

func (r *OpsManagerReconciler) createNewAppDBReconciler(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (*ReconcileAppDbReplicaSet, error) {
	return NewAppDBReplicaSetReconciler(ctx, r.imageUrls, r.initAppdbVersion, opsManager.Spec.AppDB, r.ReconcileCommonController, r.omConnectionFactory, opsManager.Annotations, r.memberClustersMap, log)
}

// getAnnotationsForOpsManagerResource returns all the annotations that should be applied to the resource
// at the end of the reconciliation.
func getAnnotationsForOpsManagerResource(opsManager *omv1.MongoDBOpsManager) (map[string]string, error) {
	finalAnnotations := make(map[string]string)
	specBytes, err := json.Marshal(opsManager.Spec)
	if err != nil {
		return nil, err
	}
	finalAnnotations[util.LastAchievedSpec] = string(specBytes)
	return finalAnnotations, nil
}
