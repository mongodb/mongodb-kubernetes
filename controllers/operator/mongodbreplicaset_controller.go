package operator

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	rolev1 "github.com/mongodb/mongodb-kubernetes/api/v1/role"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/deployment"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/host"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/replicaset"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connection"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/controlledfeature"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/create"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/recovery"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mcoConstruct "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/annotations"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/scale"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	util_int "github.com/mongodb/mongodb-kubernetes/pkg/util/int"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault/vaultwatcher"
)

// ReconcileMongoDbReplicaSet reconciles a MongoDB with a type of ReplicaSet.
// WARNING: do not put any mutable state into this struct.
// Controller runtime uses and shares a single instance of it.
type ReconcileMongoDbReplicaSet struct {
	*ReconcileCommonController
	omConnectionFactory       om.ConnectionFactory
	imageUrls                 images.ImageUrls
	forceEnterprise           bool
	enableClusterMongoDBRoles bool

	initDatabaseNonStaticImageVersion string
	databaseNonStaticImageVersion     string

	agentDebug      bool
	agentDebugImage string
}

type replicaSetDeploymentState struct {
	LastAchievedSpec         *mdbv1.MongoDbSpec
	LastReconcileMemberCount int
	LastConfiguredRoles      []string
}

var _ reconcile.Reconciler = &ReconcileMongoDbReplicaSet{}

// ReplicaSetReconcilerHelper contains state and logic for a SINGLE reconcile execution.
// This object is NOT shared between reconcile invocations.
type ReplicaSetReconcilerHelper struct {
	resource        *mdbv1.MongoDB
	deploymentState *replicaSetDeploymentState
	reconciler      *ReconcileMongoDbReplicaSet
	log             *zap.SugaredLogger
}

func (r *ReconcileMongoDbReplicaSet) newReconcilerHelper(
	ctx context.Context,
	rs *mdbv1.MongoDB,
	log *zap.SugaredLogger,
) (*ReplicaSetReconcilerHelper, error) {
	helper := &ReplicaSetReconcilerHelper{
		resource:   rs,
		reconciler: r,
		log:        log,
	}

	if err := helper.initialize(ctx); err != nil {
		return nil, err
	}

	return helper, nil
}

// readState abstract reading the state of the resource that we store on the cluster between reconciliations.
func (r *ReplicaSetReconcilerHelper) readState() (*replicaSetDeploymentState, error) {
	// Try to get the last achieved spec from annotations and store it in state
	lastAchievedSpec, err := r.resource.GetLastSpec()
	if err != nil {
		return nil, err
	}

	// Read current member count from Status once at initialization. This provides a stable view throughout
	// reconciliation and prepares for eventually storing this in ConfigMap state instead of ephemeral status.
	lastReconcileMemberCount := r.resource.Status.Members

	lastConfiguredRoles, err := r.resource.GetLastConfiguredRoles()
	if err != nil {
		return nil, err
	}

	return &replicaSetDeploymentState{
		LastAchievedSpec:         lastAchievedSpec,
		LastReconcileMemberCount: lastReconcileMemberCount,
		LastConfiguredRoles:      lastConfiguredRoles,
	}, nil
}

// getVaultAnnotations gets vault secret version annotations to write to the CR.
func (r *ReplicaSetReconcilerHelper) getVaultAnnotations() map[string]string {
	if !vault.IsVaultSecretBackend() {
		return nil
	}

	vaultMap := make(map[string]string)
	secrets := r.resource.GetSecretsMountedIntoDBPod()

	for _, s := range secrets {
		path := fmt.Sprintf("%s/%s/%s", r.reconciler.VaultClient.DatabaseSecretMetadataPath(),
			r.resource.Namespace, s)
		vaultMap = merge.StringToStringMap(vaultMap, r.reconciler.VaultClient.GetSecretAnnotation(path))
	}

	path := fmt.Sprintf("%s/%s/%s", r.reconciler.VaultClient.OperatorScretMetadataPath(),
		r.resource.Namespace, r.resource.Spec.Credentials)
	vaultMap = merge.StringToStringMap(vaultMap, r.reconciler.VaultClient.GetSecretAnnotation(path))

	return vaultMap
}

func (r *ReplicaSetReconcilerHelper) initialize(ctx context.Context) error {
	state, err := r.readState()
	if err != nil {
		return xerrors.Errorf("failed to initialize replica set state: %w", err)
	}
	r.deploymentState = state
	return nil
}

// updateStatus is a pass-through method that calls the reconciler updateStatus.
// In the future (multi-cluster epic), this will be enhanced to write deployment state to ConfigMap after every status
// update (similar to sharded cluster pattern), but for now it just delegates to maintain the same architecture.
func (r *ReplicaSetReconcilerHelper) updateStatus(ctx context.Context, status workflow.Status, statusOptions ...mdbstatus.Option) (reconcile.Result, error) {
	return r.reconciler.updateStatus(ctx, r.resource, status, r.log, statusOptions...)
}

// Reconcile performs the full reconciliation logic for a replica set.
// This is the main entry point for all reconciliation work and contains all
// state and logic specific to a single reconcile execution.
func (r *ReplicaSetReconcilerHelper) Reconcile(ctx context.Context) (reconcile.Result, error) {
	rs := r.resource
	log := r.log
	reconciler := r.reconciler

	// === 1. Initial Checks and setup
	if !architectures.IsRunningStaticArchitecture(rs.Annotations) {
		agents.UpgradeAllIfNeeded(ctx, agents.ClientSecret{Client: reconciler.client, SecretClient: reconciler.SecretClient}, reconciler.omConnectionFactory, GetWatchedNamespace(), false)
	}

	log.Info("-> ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs.Spec, "desiredReplicas", scale.ReplicasThisReconciliation(rs), "isScaling", scale.IsStillScaling(rs))
	log.Infow("ReplicaSet.Status", "status", rs.Status)

	if err := rs.ProcessValidationsOnReconcile(nil); err != nil {
		return r.updateStatus(ctx, workflow.Invalid("%s", err.Error()))
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, reconciler.client, reconciler.SecretClient, rs, log)
	if err != nil {
		return r.updateStatus(ctx, workflow.Failed(err))
	}

	conn, _, err := connection.PrepareOpsManagerConnection(ctx, reconciler.SecretClient, projectConfig, credsConfig, reconciler.omConnectionFactory, rs.Namespace, log)
	if err != nil {
		return r.updateStatus(ctx, workflow.Failed(xerrors.Errorf("failed to prepare Ops Manager connection: %w", err)))
	}

	if status := ensureSupportedOpsManagerVersion(conn); status.Phase() != mdbstatus.PhaseRunning {
		return r.updateStatus(ctx, status)
	}

	reconciler.SetupCommonWatchers(rs, nil, nil, rs.Name)

	reconcileResult := checkIfHasExcessProcesses(conn, rs.Name, log)
	if !reconcileResult.IsOK() {
		return r.updateStatus(ctx, reconcileResult)
	}

	if status := validateMongoDBResource(rs, conn); !status.IsOK() {
		return r.updateStatus(ctx, status)
	}

	if status := controlledfeature.EnsureFeatureControls(*rs, conn, conn.OpsManagerVersion(), log); !status.IsOK() {
		return r.updateStatus(ctx, status)
	}

	// === 2. Auth and Certificates
	// Get certificate paths for later use
	rsCertsConfig := certs.ReplicaSetConfig(*rs)
	var databaseSecretPath string
	if reconciler.VaultClient != nil {
		databaseSecretPath = reconciler.VaultClient.DatabaseSecretPath()
	}
	tlsCertHash := enterprisepem.ReadHashFromSecret(ctx, reconciler.SecretClient, rs.Namespace, rsCertsConfig.CertSecretName, databaseSecretPath, log)
	internalClusterCertHash := enterprisepem.ReadHashFromSecret(ctx, reconciler.SecretClient, rs.Namespace, rsCertsConfig.InternalClusterSecretName, databaseSecretPath, log)

	tlsCertPath := ""
	internalClusterCertPath := ""
	if internalClusterCertHash != "" {
		internalClusterCertPath = fmt.Sprintf("%s%s", util.InternalClusterAuthMountPath, internalClusterCertHash)
	}
	if tlsCertHash != "" {
		tlsCertPath = fmt.Sprintf("%s/%s", util.TLSCertMountPath, tlsCertHash)
	}

	agentCertSecretName := rs.GetSecurity().AgentClientCertificateSecretName(rs.Name)
	agentCertHash, agentCertPath := reconciler.agentCertHashAndPath(ctx, log, rs.Namespace, agentCertSecretName, databaseSecretPath)

	prometheusCertHash, err := certs.EnsureTLSCertsForPrometheus(ctx, reconciler.SecretClient, rs.GetNamespace(), rs.GetPrometheus(), certs.Database, log)
	if err != nil {
		return r.updateStatus(ctx, workflow.Failed(xerrors.Errorf("could not generate certificates for Prometheus: %w", err)))
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return r.updateStatus(ctx, workflow.Failed(xerrors.Errorf("failed to get agent auth mode: %w", err)))
	}

	// Check if we need to prepare for scale-down
	if scale.ReplicasThisReconciliation(rs) < r.deploymentState.LastReconcileMemberCount {
		if err := replicaset.PrepareScaleDownFromMongoDB(conn, rs, log); err != nil {
			return r.updateStatus(ctx, workflow.Failed(xerrors.Errorf("failed to prepare Replica Set for scaling down using Ops Manager: %w", err)))
		}
	}
	deploymentOpts := deploymentOptionsRS{
		prometheusCertHash:   prometheusCertHash,
		agentCertPath:        agentCertPath,
		agentCertHash:        agentCertHash,
		currentAgentAuthMode: currentAgentAuthMode,
	}

	// 3. Search Overrides
	// Apply search overrides early so searchCoordinator role is present before ensureRoles runs
	// This must happen before the ordering logic to ensure roles are synced regardless of order
	shouldMirrorKeyfileForMongot := r.applySearchOverrides(ctx)

	// 4. Recovery
	// Recovery prevents some deadlocks that can occur during reconciliation, e.g. the setting of an incorrect automation
	// configuration and a subsequent attempt to overwrite it later, the operator would be stuck in Pending phase.
	// See CLOUDP-189433 and CLOUDP-229222 for more details.
	if recovery.ShouldTriggerRecovery(rs.Status.Phase != mdbstatus.PhaseRunning, rs.Status.LastTransition) {
		log.Warnf("Triggering Automatic Recovery. The MongoDB resource %s/%s is in %s state since %s", rs.Namespace, rs.Name, rs.Status.Phase, rs.Status.LastTransition)
		automationConfigStatus := r.updateOmDeploymentRs(ctx, conn, r.deploymentState.LastReconcileMemberCount, tlsCertPath, internalClusterCertPath, deploymentOpts, shouldMirrorKeyfileForMongot, true).OnErrorPrepend("failed to create/update (Ops Manager reconciliation phase):")
		reconcileStatus := r.reconcileMemberResources(ctx, conn, projectConfig, deploymentOpts, r.deploymentState.LastConfiguredRoles)
		if !reconcileStatus.IsOK() {
			log.Errorf("Recovery failed because of reconcile errors, %v", reconcileStatus)
		}
		if !automationConfigStatus.IsOK() {
			log.Errorf("Recovery failed because of Automation Config update errors, %v", automationConfigStatus)
		}
	}

	// 5. Actual reconciliation execution, Ops Manager and kubernetes resources update
	publishAutomationConfigFirst := publishAutomationConfigFirstRS(ctx, reconciler.client, *rs, r.deploymentState.LastAchievedSpec, deploymentOpts.currentAgentAuthMode, projectConfig.SSLMMSCAConfigMap, log)
	status := workflow.RunInGivenOrder(publishAutomationConfigFirst,
		func() workflow.Status {
			return r.updateOmDeploymentRs(ctx, conn, r.deploymentState.LastReconcileMemberCount, tlsCertPath, internalClusterCertPath, deploymentOpts, shouldMirrorKeyfileForMongot, false).OnErrorPrepend("failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			return r.reconcileMemberResources(ctx, conn, projectConfig, deploymentOpts, r.deploymentState.LastConfiguredRoles)
		})

	if !status.IsOK() {
		return r.updateStatus(ctx, status)
	}

	// === 6. Final steps
	if scale.IsStillScaling(rs) {
		return r.updateStatus(ctx, workflow.Pending("Continuing scaling operation for ReplicaSet %s, desiredMembers=%d, currentMembers=%d", rs.ObjectKey(), rs.DesiredReplicas(), scale.ReplicasThisReconciliation(rs)), mdbstatus.MembersOption(rs))
	}

	// Get lastspec, vault annotations when needed and write them to the resource.
	// These operations should only be performed on successful reconciliations.
	// The state of replica sets is currently split between the annotations and the member count in status. Both should
	// be migrated to config maps
	annotationsToAdd, err := getAnnotationsForResource(r.resource)
	if err != nil {
		return r.updateStatus(ctx, workflow.Failed(xerrors.Errorf("could not get resource annotations: %w", err)))
	}

	for k, val := range r.getVaultAnnotations() {
		annotationsToAdd[k] = val
	}

	roleAnnotation, _, err := r.reconciler.getRoleAnnotation(ctx, r.resource.Spec.DbCommonSpec, r.reconciler.enableClusterMongoDBRoles, kube.ObjectKeyFromApiObject(r.resource))
	if err != nil {
		return r.updateStatus(ctx, workflow.Failed(err))
	}
	for k, val := range roleAnnotation {
		annotationsToAdd[k] = val
	}

	if err := annotations.SetAnnotations(ctx, r.resource, annotationsToAdd, r.reconciler.client); err != nil {
		return r.updateStatus(ctx, workflow.Failed(xerrors.Errorf("could not update resource annotations: %w", err)))
	}

	log.Infof("Finished reconciliation for MongoDbReplicaSet! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return r.updateStatus(ctx, workflow.OK(), mdbstatus.NewBaseUrlOption(deployment.Link(conn.BaseURL(), conn.GroupID())), mdbstatus.MembersOption(rs), mdbstatus.NewPVCsStatusOptionEmptyStatus())
}

func newReplicaSetReconciler(ctx context.Context, kubeClient client.Client, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise, enableClusterMongoDBRoles, agentDebug bool, agentDebugImage string, omFunc om.ConnectionFactory) *ReconcileMongoDbReplicaSet {
	return &ReconcileMongoDbReplicaSet{
		ReconcileCommonController: NewReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:       omFunc,
		imageUrls:                 imageUrls,
		forceEnterprise:           forceEnterprise,
		enableClusterMongoDBRoles: enableClusterMongoDBRoles,

		initDatabaseNonStaticImageVersion: initDatabaseNonStaticImageVersion,
		databaseNonStaticImageVersion:     databaseNonStaticImageVersion,

		agentDebug:      agentDebug,
		agentDebugImage: agentDebugImage,
	}
}

type deploymentOptionsRS struct {
	agentCertPath        string
	agentCertHash        string
	prometheusCertHash   string
	currentAgentAuthMode string
}

// Generic Kubernetes Resources
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=list;watch,namespace=placeholder
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch,namespace=placeholder
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update,namespace=placeholder
// +kubebuilder:rbac:groups=core,resources={secrets,configmaps},verbs=get;list;watch;create;delete;update,namespace=placeholder
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=create;get;list;watch;delete;update,namespace=placeholder

// MongoDB Resource
// +kubebuilder:rbac:groups=mongodb.com,resources={mongodb,mongodb/status,mongodb/finalizers},verbs=*,namespace=placeholder

// Setting up a webhook
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;create;update;delete

// Certificate generation
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=get;create;list;watch

// Reconcile reads that state of the cluster for a MongoDbReplicaSet object and makes changes based on the state read
// and what is in the MongoDbReplicaSet.Spec
func (r *ReconcileMongoDbReplicaSet) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("ReplicaSet", request.NamespacedName)
	rs := &mdbv1.MongoDB{}

	if reconcileResult, err := r.prepareResourceForReconciliation(ctx, request, rs, log); err != nil {
		if errors.IsNotFound(err) {
			return workflow.Invalid("Object for reconciliation not found").ReconcileResult()
		}
		return reconcileResult, err
	}

	// Create helper for THIS reconciliation
	helper, err := r.newReconcilerHelper(ctx, rs, log)
	if err != nil {
		return r.updateStatus(ctx, rs, workflow.Failed(err), log)
	}

	// Delegate all reconciliation logic to helper
	return helper.Reconcile(ctx)
}

func publishAutomationConfigFirstRS(ctx context.Context, getter kubernetesClient.Client, mdb mdbv1.MongoDB, lastSpec *mdbv1.MongoDbSpec, currentAgentAuthMode string, sslMMSCAConfigMap string, log *zap.SugaredLogger) bool {
	namespacedName := kube.ObjectKey(mdb.Namespace, mdb.Name)
	currentSts, err := getter.GetStatefulSet(ctx, namespacedName)
	if err != nil {
		if errors.IsNotFound(err) {
			// No need to publish state as this is a new StatefulSet
			log.Debugf("New StatefulSet %s", namespacedName)
			return false
		}

		log.Debugw(fmt.Sprintf("Error getting StatefulSet %s", namespacedName), "error", err)
		return false
	}

	databaseContainer := container.GetByName(util.DatabaseContainerName, currentSts.Spec.Template.Spec.Containers)
	volumeMounts := databaseContainer.VolumeMounts

	if !mdb.Spec.Security.IsTLSEnabled() && wasTLSSecretMounted(ctx, getter, currentSts, mdb, log) {
		log.Debug(automationConfigFirstMsg("security.tls.enabled", "false"))
		return true
	}

	if mdb.Spec.Security.TLSConfig.CA == "" && wasCAConfigMapMounted(ctx, getter, currentSts, mdb, log) {
		log.Debug(automationConfigFirstMsg("security.tls.CA", "empty"))
		return true
	}

	if sslMMSCAConfigMap == "" && statefulset.VolumeMountWithNameExists(volumeMounts, construct.CaCertName) {
		log.Debug(automationConfigFirstMsg("SSLMMSCAConfigMap", "empty"))
		return true
	}

	if mdb.Spec.Security.GetAgentMechanism(currentAgentAuthMode) != util.X509 && statefulset.VolumeMountWithNameExists(volumeMounts, util.AgentSecretName) {
		log.Debug(automationConfigFirstMsg("project.AuthMode", "empty"))
		return true
	}

	if mdb.Spec.Members < int(*currentSts.Spec.Replicas) {
		log.Debug("Scaling down operation. automationConfig needs to be updated first")
		return true
	}

	if architectures.IsRunningStaticArchitecture(mdb.GetAnnotations()) {
		if mdb.Spec.IsInChangeVersion(lastSpec) {
			return true
		}
	}

	return false
}

func getHostnameOverrideConfigMapForReplicaset(mdb *mdbv1.MongoDB) corev1.ConfigMap {
	data := make(map[string]string)

	if mdb.Spec.DbCommonSpec.GetExternalDomain() != nil {
		hostnames, names := dns.GetDNSNames(mdb.Name, "", mdb.GetObjectMeta().GetNamespace(), mdb.Spec.GetClusterDomain(), mdb.Spec.Members, mdb.Spec.DbCommonSpec.GetExternalDomain())
		for i := range hostnames {
			data[names[i]] = hostnames[i]
		}
	}

	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-hostname-override", mdb.Name),
			Namespace: mdb.Namespace,
		},
		Data: data,
	}
	return cm
}

func (r *ReplicaSetReconcilerHelper) reconcileHostnameOverrideConfigMap(ctx context.Context, log *zap.SugaredLogger, getUpdateCreator configmap.GetUpdateCreator) error {
	if r.resource.Spec.DbCommonSpec.GetExternalDomain() == nil {
		return nil
	}

	cm := getHostnameOverrideConfigMapForReplicaset(r.resource)
	err := configmap.CreateOrUpdate(ctx, getUpdateCreator, cm)
	if err != nil && !errors.IsAlreadyExists(err) {
		return xerrors.Errorf("failed to create configmap: %s, err: %w", cm.Name, err)
	}
	log.Infof("Successfully ensured configmap: %s", cm.Name)

	return nil
}

// reconcileMemberResources handles the synchronization of kubernetes resources, which can be statefulsets, services etc.
// All the resources required in the k8s cluster (as opposed to the automation config) for creating the replicaset
// should be reconciled in this method.
func (r *ReplicaSetReconcilerHelper) reconcileMemberResources(ctx context.Context, conn om.Connection, projectConfig mdbv1.ProjectConfig, deploymentOptions deploymentOptionsRS, lastConfiguredRoles []string) workflow.Status {
	rs := r.resource
	reconciler := r.reconciler
	log := r.log

	// Reconcile hostname override ConfigMap
	if err := r.reconcileHostnameOverrideConfigMap(ctx, log, r.reconciler.client); err != nil {
		return workflow.Failed(xerrors.Errorf("failed to reconcile hostname override ConfigMap: %w", err))
	}

	// Ensure roles are properly configured
	if status := reconciler.ensureRoles(ctx, rs.Spec.DbCommonSpec, reconciler.enableClusterMongoDBRoles, conn, kube.ObjectKeyFromApiObject(rs), lastConfiguredRoles, log); !status.IsOK() {
		return status
	}

	return r.reconcileStatefulSet(ctx, conn, projectConfig, deploymentOptions)
}

func (r *ReplicaSetReconcilerHelper) reconcileStatefulSet(ctx context.Context, conn om.Connection, projectConfig mdbv1.ProjectConfig, deploymentOptions deploymentOptionsRS) workflow.Status {
	rs := r.resource
	reconciler := r.reconciler
	log := r.log

	certConfigurator := certs.ReplicaSetX509CertConfigurator{MongoDB: rs, SecretClient: reconciler.SecretClient}
	status := reconciler.ensureX509SecretAndCheckTLSType(ctx, certConfigurator, deploymentOptions.currentAgentAuthMode, log)
	if !status.IsOK() {
		return status
	}

	status = certs.EnsureSSLCertsForStatefulSet(ctx, reconciler.SecretClient, reconciler.SecretClient, *rs.Spec.Security, certs.ReplicaSetConfig(*rs), log)
	if !status.IsOK() {
		return status
	}

	// Build the replica set config
	rsConfig, err := r.buildStatefulSetOptions(ctx, conn, projectConfig, deploymentOptions)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("failed to build StatefulSet options: %w", err))
	}

	sts := construct.DatabaseStatefulSet(*rs, rsConfig, log)

	// Handle PVC resize if needed
	if workflowStatus := r.handlePVCResize(ctx, &sts); !workflowStatus.IsOK() {
		return workflowStatus
	}

	// Create or update the StatefulSet in Kubernetes
	if err := create.DatabaseInKubernetes(ctx, reconciler.client, *rs, sts, rsConfig, log); err != nil {
		return workflow.Failed(xerrors.Errorf("failed to create/update (Kubernetes reconciliation phase): %w", err))
	}

	// Check StatefulSet status
	if status := statefulset.GetStatefulSetStatus(ctx, rs.Namespace, rs.Name, reconciler.client); !status.IsOK() {
		return status
	}

	log.Info("Updated StatefulSet for replica set")
	return workflow.OK()
}

func (r *ReplicaSetReconcilerHelper) handlePVCResize(ctx context.Context, sts *appsv1.StatefulSet) workflow.Status {
	workflowStatus := create.HandlePVCResize(ctx, r.reconciler.client, sts, r.log)
	if !workflowStatus.IsOK() {
		return workflowStatus
	}

	if workflow.ContainsPVCOption(workflowStatus.StatusOptions()) {
		if _, err := r.reconciler.updateStatus(ctx, r.resource, workflow.Pending(""), r.log, workflowStatus.StatusOptions()...); err != nil {
			return workflow.Failed(xerrors.Errorf("error updating status: %w", err))
		}
	}
	return workflow.OK()
}

// buildStatefulSetOptions creates the options needed for constructing the StatefulSet
func (r *ReplicaSetReconcilerHelper) buildStatefulSetOptions(ctx context.Context, conn om.Connection, projectConfig mdbv1.ProjectConfig, deploymentOptions deploymentOptionsRS) (func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, error) {
	rs := r.resource
	reconciler := r.reconciler
	log := r.log

	rsCertsConfig := certs.ReplicaSetConfig(*rs)

	var vaultConfig vault.VaultConfiguration
	var databaseSecretPath string
	if reconciler.VaultClient != nil {
		vaultConfig = reconciler.VaultClient.VaultConfig
		databaseSecretPath = reconciler.VaultClient.DatabaseSecretPath()
	}

	// Determine automation agent version for static architecture
	var automationAgentVersion string
	if architectures.IsRunningStaticArchitecture(rs.Annotations) {
		// In case the Agent *is* overridden, its version will be merged into the StatefulSet. The merging process
		// happens after creating the StatefulSet definition.
		if !rs.IsAgentImageOverridden() {
			var err error
			automationAgentVersion, err = reconciler.getAgentVersion(conn, conn.OpsManagerVersion().VersionString, false, log)
			if err != nil {
				return nil, xerrors.Errorf("impossible to get agent version, please override the agent image by providing a pod template: %w", err)
			}
		}
	}

	tlsCertHash := enterprisepem.ReadHashFromSecret(ctx, reconciler.SecretClient, rs.Namespace, rsCertsConfig.CertSecretName, databaseSecretPath, log)
	internalClusterCertHash := enterprisepem.ReadHashFromSecret(ctx, reconciler.SecretClient, rs.Namespace, rsCertsConfig.InternalClusterSecretName, databaseSecretPath, log)

	rsConfig := construct.ReplicaSetOptions(
		PodEnvVars(newPodVars(conn, projectConfig, rs.Spec.LogLevel)),
		CurrentAgentAuthMechanism(deploymentOptions.currentAgentAuthMode),
		CertificateHash(tlsCertHash),
		AgentCertHash(deploymentOptions.agentCertHash),
		InternalClusterHash(internalClusterCertHash),
		PrometheusTLSCertHash(deploymentOptions.prometheusCertHash),
		WithVaultConfig(vaultConfig),
		WithLabels(rs.Labels),
		WithAdditionalMongodConfig(rs.Spec.GetAdditionalMongodConfig()),
		WithInitDatabaseNonStaticImage(images.ContainerImage(reconciler.imageUrls, util.InitDatabaseImageUrlEnv, reconciler.initDatabaseNonStaticImageVersion)),
		WithDatabaseNonStaticImage(images.ContainerImage(reconciler.imageUrls, util.NonStaticDatabaseEnterpriseImage, reconciler.databaseNonStaticImageVersion)),
		WithAgentImage(images.ContainerImage(reconciler.imageUrls, architectures.MdbAgentImageRepo, automationAgentVersion)),
		WithMongodbImage(images.GetOfficialImage(reconciler.imageUrls, rs.Spec.Version, rs.GetAnnotations())),
		WithAgentDebug(reconciler.agentDebug),
		WithAgentDebugImage(reconciler.agentDebugImage),
	)

	return rsConfig, nil
}

// AddReplicaSetController creates a new MongoDbReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddReplicaSetController(ctx context.Context, mgr manager.Manager, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise, enableClusterMongoDBRoles, agentDebug bool, agentDebugImage string) error {
	// Create a new controller
	reconciler := newReplicaSetReconciler(ctx, mgr.GetClient(), imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, agentDebug, agentDebugImage, om.NewOpsManagerConnection)
	c, err := controller.New(util.MongoDbReplicaSetController, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}) // nolint:forbidigo
	if err != nil {
		return err
	}

	// watch for changes to replica set MongoDB resources
	eventHandler := ResourceEventHandler{deleter: reconciler}
	// Watch for changes to primary resource MongoDbReplicaSet
	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbv1.MongoDB{}, &eventHandler, watch.PredicatesForMongoDB(mdbv1.ReplicaSet)))
	if err != nil {
		return err
	}

	err = c.Watch(source.Channel(OmUpdateChannel, &handler.EnqueueRequestForObject{}, source.WithPredicates[client.Object, reconcile.Request](watch.PredicatesForMongoDB(mdbv1.ReplicaSet))))
	if err != nil {
		return xerrors.Errorf("not able to setup OmUpdateChannel to listent to update events from OM: %s", err)
	}

	err = c.Watch(
		source.Kind[client.Object](mgr.GetCache(), &appsv1.StatefulSet{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &mdbv1.MongoDB{}, handler.OnlyControllerOwner()),
			watch.PredicatesForStatefulSet()))
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.ConfigMap{},
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.Secret{},
		&watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	if enableClusterMongoDBRoles {
		err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &rolev1.ClusterMongoDBRole{},
			&watch.ResourcesHandler{ResourceType: watch.ClusterMongoDBRole, ResourceWatcher: reconciler.resourceWatcher}))
		if err != nil {
			return err
		}
	}

	// if vault secret backend is enabled watch for Vault secret change and trigger reconcile
	if vault.IsVaultSecretBackend() {
		eventChannel := make(chan event.GenericEvent)
		go vaultwatcher.WatchSecretChangeForMDB(ctx, zap.S(), eventChannel, reconciler.client, reconciler.VaultClient, mdbv1.ReplicaSet)

		err = c.Watch(source.Channel[client.Object](eventChannel, &handler.EnqueueRequestForObject{}))
		if err != nil {
			zap.S().Errorf("Failed to watch for vault secret changes: %w", err)
		}
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &searchv1.MongoDBSearch{},
		handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, search *searchv1.MongoDBSearch) []reconcile.Request {
			source := search.GetMongoDBResourceRef()
			if source == nil {
				return []reconcile.Request{}
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: source.Namespace, Name: source.Name}}}
		})))
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbReplicaSetController)

	return nil
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (r *ReplicaSetReconcilerHelper) updateOmDeploymentRs(ctx context.Context, conn om.Connection, membersNumberBefore int, tlsCertPath, internalClusterCertPath string, deploymentOptions deploymentOptionsRS, shouldMirrorKeyfileForMongot bool, isRecovering bool) workflow.Status {
	rs := r.resource
	log := r.log
	reconciler := r.reconciler
	log.Debug("Entering UpdateOMDeployments")
	// Only "concrete" RS members should be observed
	// - if scaling down, let's observe only members that will remain after scale-down operation
	// - if scaling up, observe only current members, because new ones might not exist yet
	replicasTarget := scale.ReplicasThisReconciliation(rs)
	err := agents.WaitForRsAgentsToRegisterByResource(rs, util_int.Min(membersNumberBefore, replicasTarget), conn, log)
	if err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	caFilePath := fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)

	replicaSet := replicaset.BuildFromMongoDBWithReplicas(reconciler.imageUrls[mcoConstruct.MongodbImageEnv], reconciler.forceEnterprise, rs, replicasTarget, rs.CalculateFeatureCompatibilityVersion(), tlsCertPath)
	processNames := replicaSet.GetProcessNames()

	status, additionalReconciliationRequired := reconciler.updateOmAuthentication(ctx, conn, processNames, rs, deploymentOptions.agentCertPath, caFilePath, internalClusterCertPath, isRecovering, log)
	if !status.IsOK() && !isRecovering {
		return status
	}

	lastRsConfig, err := mdbv1.GetLastAdditionalMongodConfigByType(r.deploymentState.LastAchievedSpec, mdbv1.ReplicaSetConfig)
	if err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	prometheusConfiguration := PrometheusConfiguration{
		prometheus:         rs.GetPrometheus(),
		conn:               conn,
		secretsClient:      reconciler.SecretClient,
		namespace:          rs.GetNamespace(),
		prometheusCertHash: deploymentOptions.prometheusCertHash,
	}

	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			if shouldMirrorKeyfileForMongot {
				if err := r.mirrorKeyfileIntoSecretForMongot(ctx, d); err != nil {
					return err
				}
			}
			return ReconcileReplicaSetAC(ctx, d, rs.Spec.DbCommonSpec, lastRsConfig.ToMap(), rs.Name, replicaSet, caFilePath, internalClusterCertPath, &prometheusConfiguration, log)
		},
		log,
	)

	if err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	if err := om.WaitForReadyState(conn, processNames, isRecovering, log); err != nil {
		return workflow.Failed(err)
	}

	reconcileResult, _ := ReconcileLogRotateSetting(conn, rs.Spec.Agent, log)
	if !reconcileResult.IsOK() {
		return reconcileResult
	}

	if additionalReconciliationRequired {
		return workflow.Pending("Performing multi stage reconciliation")
	}

	// Monitoring hosts reconciliation
	// Compare actually monitored hosts against desired hosts and remove any extras.
	// This runs on EVERY reconciliation (not just scale-down) to ensure idempotency
	// and self-healing of orphaned hosts from previous failed reconciliations.
	hostsDesired, _ := dns.GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain(),
		replicasTarget, rs.Spec.DbCommonSpec.GetExternalDomain())
	serviceFQDN := dns.GetServiceFQDN(rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain())
	// TODO: should we fail the reconciliation if we fail this cleanup ? Or should we just display a warning ?
	if err := host.RemoveUndesiredMonitoringHosts(conn, rs.Name, serviceFQDN, hostsDesired, log); err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	if status := reconciler.ensureBackupConfigurationAndUpdateStatus(ctx, conn, rs, reconciler.SecretClient, log); !status.IsOK() && !isRecovering {
		return status
	}

	log.Info("Updated Ops Manager for replica set")
	return workflow.OK()
}

func (r *ReplicaSetReconcilerHelper) OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error {
	rs := obj.(*mdbv1.MongoDB)

	if err := r.cleanOpsManagerState(ctx, rs, log); err != nil {
		return err
	}

	r.reconciler.resourceWatcher.RemoveDependentWatchedResources(rs.ObjectKey())

	return nil
}

func (r *ReplicaSetReconcilerHelper) cleanOpsManagerState(ctx context.Context, rs *mdbv1.MongoDB, log *zap.SugaredLogger) error {
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.reconciler.client, r.reconciler.SecretClient, rs, log)
	if err != nil {
		return err
	}

	log.Infow("Removing replica set from Ops Manager", "config", rs.Spec)
	conn, _, err := connection.PrepareOpsManagerConnection(ctx, r.reconciler.SecretClient, projectConfig, credsConfig, r.reconciler.omConnectionFactory, rs.Namespace, log)
	if err != nil {
		return err
	}

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			processNames = d.GetProcessNames(om.ReplicaSet{}, rs.Name)
			// error means that replica set is not in the deployment - it's ok, and we can proceed (could happen if
			// deletion cleanup happened twice and the first one cleaned OM state already)
			if e := d.RemoveReplicaSetByName(rs.Name, log); e != nil {
				log.Warnf("Failed to remove replica set from automation config: %s", e)
			}

			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, processNames, false, log); err != nil {
		return err
	}

	if rs.Spec.Backup != nil && rs.Spec.Backup.AutoTerminateOnDeletion {
		if err := backup.StopBackupIfEnabled(conn, conn, rs.Name, backup.ReplicaSetType, log); err != nil {
			return err
		}
	}

	// During deletion, calculate the maximum number of hosts that could possibly exist to ensure complete cleanup.
	// Reading from Status here is appropriate since this is outside the reconciliation loop.
	hostsToRemove, _ := dns.GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain(), util.MaxInt(rs.Status.Members, rs.Spec.Members), nil)
	log.Infow("Stop monitoring removed hosts in Ops Manager", "removedHosts", hostsToRemove)

	if err = host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	if err := r.reconciler.clearProjectAuthenticationSettings(ctx, conn, rs, processNames, log); err != nil {
		return err
	}

	log.Infow("Clear feature control for group: %s", "groupID", conn.GroupID())
	if result := controlledfeature.ClearFeatureControls(conn, conn.OpsManagerVersion(), log); !result.IsOK() {
		result.Log(log)
		log.Warnf("Failed to clear feature control from group: %s", conn.GroupID())
	}

	log.Info("Removed replica set from Ops Manager!")
	return nil
}

func (r *ReconcileMongoDbReplicaSet) OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error {
	helper, err := r.newReconcilerHelper(ctx, obj.(*mdbv1.MongoDB), log)
	if err != nil {
		return err
	}
	return helper.OnDelete(ctx, obj, log)
}

func (r *ReplicaSetReconcilerHelper) applySearchOverrides(ctx context.Context) bool {
	rs := r.resource
	log := r.log

	search := r.lookupCorrespondingSearchResource(ctx)
	if search == nil {
		log.Debugf("No MongoDBSearch resource found, skipping search overrides")
		return false
	}

	log.Infof("Applying search overrides from MongoDBSearch %s", search.NamespacedName())

	if rs.Spec.AdditionalMongodConfig == nil {
		rs.Spec.AdditionalMongodConfig = mdbv1.NewEmptyAdditionalMongodConfig()
	}
	searchMongodConfig := searchcontroller.GetMongodConfigParameters(search, rs.Spec.GetClusterDomain())
	rs.Spec.AdditionalMongodConfig.AddOption("setParameter", searchMongodConfig["setParameter"])

	return true
}

func (r *ReplicaSetReconcilerHelper) mirrorKeyfileIntoSecretForMongot(ctx context.Context, d om.Deployment) error {
	rs := r.resource
	reconciler := r.reconciler
	log := r.log

	keyfileContents := maputil.ReadMapValueAsString(d, "auth", "key")
	keyfileSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", rs.Name, searchcontroller.MongotKeyfileFilename), Namespace: rs.Namespace}}

	log.Infof("Mirroring the replicaset %s's keyfile into the secret %s", rs.ObjectKey(), kube.ObjectKeyFromApiObject(keyfileSecret))

	_, err := controllerutil.CreateOrUpdate(ctx, reconciler.client, keyfileSecret, func() error {
		keyfileSecret.StringData = map[string]string{searchcontroller.MongotKeyfileFilename: keyfileContents}
		return controllerutil.SetOwnerReference(rs, keyfileSecret, reconciler.client.Scheme())
	})
	if err != nil {
		return xerrors.Errorf("failed to mirror the replicaset's keyfile into a secret: %w", err)
	}
	return nil
}

func (r *ReplicaSetReconcilerHelper) lookupCorrespondingSearchResource(ctx context.Context) *searchv1.MongoDBSearch {
	rs := r.resource
	reconciler := r.reconciler
	log := r.log

	var search *searchv1.MongoDBSearch
	searchList := &searchv1.MongoDBSearchList{}
	if err := reconciler.client.List(ctx, searchList, &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(searchcontroller.MongoDBSearchIndexFieldName, rs.GetNamespace()+"/"+rs.GetName()),
	}); err != nil {
		log.Debugf("Failed to list MongoDBSearch resources: %v", err)
	}
	// this validates that there is exactly one MongoDBSearch pointing to this resource,
	// and that this resource passes search validations. If either fails, proceed without a search target
	// for the mongod automation config.
	if len(searchList.Items) == 1 {
		searchSource := searchcontroller.NewEnterpriseResourceSearchSource(rs)
		if searchSource.Validate() == nil {
			search = &searchList.Items[0]
		}
	}
	return search
}
