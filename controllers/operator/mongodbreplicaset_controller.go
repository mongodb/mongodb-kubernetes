package operator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
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

	stderrors "errors"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	rolev1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/role"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/deployment"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/host"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/replicaset"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connection"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/controlledfeature"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/create"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/recovery"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/annotations"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/monarch/drstate"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	util_int "github.com/mongodb/mongodb-kubernetes/pkg/util/int"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/scale"
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

	agentDebug          bool
	agentDebugImage     string
	defaultArchitecture architectures.DefaultArchitecture
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
	if !architectures.IsRunningStaticArchitecture(rs.Annotations, reconciler.defaultArchitecture) {
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

	conn, agentAPIKey, err := connection.PrepareOpsManagerConnection(ctx, reconciler.SecretClient, projectConfig, credsConfig, reconciler.omConnectionFactory, rs.Namespace, true, log)
	if err != nil {
		return r.updateStatus(ctx, workflow.Failed(xerrors.Errorf("failed to prepare Ops Manager connection: %w", err)))
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

	deploymentOpts := deploymentOptionsRS{
		prometheusCertHash:   prometheusCertHash,
		agentCertPath:        agentCertPath,
		agentCertHash:        agentCertHash,
		currentAgentAuthMode: currentAgentAuthMode,
	}

	// 3. Search Overrides
	// Apply search overrides early so searchCoordinator role is present before ensureRoles runs
	// This must happen before the ordering logic to ensure roles are synced regardless of order
	shouldMirrorKeyfileForMongot, err := r.applySearchOverrides(ctx)
	if err != nil {
		return r.updateStatus(ctx, workflow.Failed(err))
	}

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

	// 5. Promoted-standby gate.
	//
	// When a CR has spec.role=active AND a DR-state file in S3 (which only
	// exists if it was provisioned as standby), this CR is mid-promotion or
	// already promoted. Per the EA Standby Clusters playbook (PP scope), the
	// operator must NOT push an active-shaped AC or reshape K8s infra in
	// this state — the standby's AC + injector Deployment from first
	// provisioning stay for the CR's lifetime. handlePromotedMonarchLifecycle
	// handles all four S3 states (Standby/PromoteStandby/StandbyReadyToPromote/Active).
	//
	// Standby CRs (spec.role=standby) always fall through to regular
	// reconciliation, even after the DR file is auto-seeded — that keeps
	// InjectorReady fresh and the K8s resources in sync.
	if status, handled := r.handlePromotedMonarchLifecycle(ctx, conn, agentAPIKey); handled {
		return r.updateStatus(ctx, status)
	}

	// 5a. Monarch pre-AC step: ensure the Service (and clean up old-role resources).
	// The Service must exist before the AC push so the DNS in maintainedMonarchComponents
	// resolves. The ConfigMap and Deployment are NOT created here — for the active
	// shipper, they need mms-shipper credentials that OM only emits *after* it sees
	// maintainedMonarchComponents in the AC. Creating the Deployment here would put
	// the shipper into CrashLoopBackOff until the next reconcile. Post-AC step below
	// creates them once OM has populated the credentials.
	if rs.Spec.Monarch != nil {
		if monarchStatus := r.reconcileMonarchPreAC(ctx); !monarchStatus.IsOK() {
			return r.updateStatus(ctx, monarchStatus)
		}
	}

	// 5. Actual reconciliation execution, Ops Manager and kubernetes resources update
	publishAutomationConfigFirst := publishAutomationConfigFirstRS(ctx, reconciler.client, *rs, r.deploymentState.LastAchievedSpec, deploymentOpts.currentAgentAuthMode, projectConfig.SSLMMSCAConfigMap, reconciler.defaultArchitecture, log)
	status := workflow.RunInGivenOrder(publishAutomationConfigFirst,
		func() workflow.Status {
			return r.updateOmDeploymentRs(ctx, conn, r.deploymentState.LastReconcileMemberCount, tlsCertPath, internalClusterCertPath, deploymentOpts, shouldMirrorKeyfileForMongot, false).OnErrorPrepend("failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			return r.reconcileMemberResources(ctx, conn, projectConfig, deploymentOpts, r.deploymentState.LastConfiguredRoles)
		})

	// 5c. Monarch post-AC step: now that the AC includes maintainedMonarchComponents,
	// fetch OM's cleartext mms-shipper credentials and create the ConfigMap + Deployment
	// in one shot. Pods come up authenticated on first start — no CrashLoopBackOff
	// window.
	if status.IsOK() && rs.Spec.Monarch != nil {
		if monarchStatus := r.reconcileMonarchPostAC(ctx, conn, agentAPIKey); !monarchStatus.IsOK() {
			status = monarchStatus
		}
	}

	// Surface agent plan failures regardless of outcome so users can diagnose stuck
	// reconciles without exec'ing into pods. Runs after the AC push (regardless of
	// status) so it reflects the most recent agent state; must be before the
	// status check below that may short-circuit on Pending/Failed.
	r.surfaceAgentPlanStatus(ctx, conn)

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
	return r.updateStatus(ctx, workflow.OK(), mdbstatus.NewBaseUrlOption(deployment.Link(conn.BaseURL(), conn.GroupID())), mdbstatus.NewProjectIdOption(conn.GroupID()), mdbstatus.MembersOption(rs), mdbstatus.NewPVCsStatusOptionEmptyStatus())
}

func newReplicaSetReconciler(ctx context.Context, kubeClient client.Client, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise, enableClusterMongoDBRoles, agentDebug bool, agentDebugImage string, defaultArchitecture architectures.DefaultArchitecture, omFunc om.ConnectionFactory) *ReconcileMongoDbReplicaSet {
	return &ReconcileMongoDbReplicaSet{
		ReconcileCommonController: NewReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:       omFunc,
		imageUrls:                 imageUrls,
		forceEnterprise:           forceEnterprise,
		enableClusterMongoDBRoles: enableClusterMongoDBRoles,

		initDatabaseNonStaticImageVersion: initDatabaseNonStaticImageVersion,
		databaseNonStaticImageVersion:     databaseNonStaticImageVersion,

		agentDebug:          agentDebug,
		agentDebugImage:     agentDebugImage,
		defaultArchitecture: defaultArchitecture,
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
		if k8serrors.IsNotFound(err) {
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

func publishAutomationConfigFirstRS(ctx context.Context, getter kubernetesClient.Client, mdb mdbv1.MongoDB, lastSpec *mdbv1.MongoDbSpec, currentAgentAuthMode string, sslMMSCAConfigMap string, defaultArchitecture architectures.DefaultArchitecture, log *zap.SugaredLogger) bool {
	namespacedName := kube.ObjectKey(mdb.Namespace, mdb.Name)
	currentSts, err := getter.GetStatefulSet(ctx, namespacedName)
	if err != nil {
		if k8serrors.IsNotFound(err) {
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

	if architectures.IsRunningStaticArchitecture(mdb.GetAnnotations(), defaultArchitecture) {
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
	if err != nil && !k8serrors.IsAlreadyExists(err) {
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

// reconcileMonarchResources ensures Monarch ConfigMap, Service, and Deployment exist.
// It owns Kubernetes resources only; the automation config push (including
// maintainedMonarchComponents) happens atomically in updateOmDeploymentRs so the
// agent sees the full standby goal state on its first AC poll.
//
// This reconcile is purely declarative on spec.monarch.role:
//   - role=active  → ensure shipper resources exist; ensure injector resources do NOT
//   - role=standby → opposite
//
// No state machine, no lastAchievedSpec lookups, no overrides. Each reconcile reads
// the spec and converges; running it again is a no-op.
//
// Failover triggers (both end up writing PromoteStandby to S3, the agent does the rest):
//   - Customer/CLI: aws s3 cp ... PromoteStandby ... (operator just observes via
//     reconcileMonarchS3State; sets SpecOutOfSync until the user updates the CR)
//   - Operator: when role=active and S3 hasn't been promoted yet, triggerPromotionIfNeeded
//     writes PromoteStandby to S3 — same destination, same agent reaction
//
// NOT supported: active → standby demotion. If the user flips role from active to
// standby, this function will dutifully provision an injector while the agent on the
// running mongod still thinks it's active. The result is broken. To remove a cluster,
// delete the MongoDB CR.
// reconcileMonarchPreAC runs *before* the AC push. It establishes the Service
// (whose DNS the AC's maintainedMonarchComponents references) and handles
// idempotent cleanup of leftover other-role resources. It deliberately does NOT
// create the ConfigMap or Deployment yet — for the active role we need
// mms-shipper credentials embedded in the ConfigMap, and OM only emits that user
// in response to seeing maintainedMonarchComponents in the AC. Creating the
// Deployment now would put the shipper into CrashLoopBackOff until the next
// reconcile, which is what we're trying to avoid.
//
// reconcileMonarchPostAC runs the rest of the work after the AC push has
// completed and OM has had a chance to populate shipperUser/shipperPwd.
func (r *ReplicaSetReconcilerHelper) reconcileMonarchPreAC(ctx context.Context) workflow.Status {
	rs := r.resource
	reconciler := r.reconciler

	// Observe S3 state and surface any CR/S3 divergence as a condition. Does not
	// drive provisioning — provisioning is purely from spec.Monarch.Role below.
	if status := r.reconcileMonarchS3State(ctx); !status.IsOK() {
		return status
	}

	role := rs.Spec.Monarch.Role
	otherRole := "shipper"
	if role == mdbv1.MonarchRoleActive {
		otherRole = "injector"
	}

	// Idempotent cleanup: ensure resources for the OTHER role don't exist. After a
	// standby→active flip this deletes the leftover injector resources. On a fresh
	// install both calls are no-ops (NotFound is treated as success).
	if status := r.deleteMonarchResourcesForRole(ctx, otherRole); !status.IsOK() {
		return status
	}

	// Fail fast if S3 credentials secret doesn't exist or is missing required keys;
	// values are consumed by the Deployment (in post-AC) and by buildMonarchComponentsForAC.
	credSecret, err := reconciler.client.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Spec.Monarch.S3.CredentialsSecretRef.Name))
	if err != nil {
		return workflow.Failed(xerrors.Errorf("failed to read Monarch credentials secret %s: %w", rs.Spec.Monarch.S3.CredentialsSecretRef.Name, err))
	}
	for _, key := range []string{"awsAccessKeyId", "awsSecretAccessKey"} {
		if _, ok := credSecret.Data[key]; !ok {
			return workflow.Failed(xerrors.Errorf("Monarch credentials secret %s missing required key %q", rs.Spec.Monarch.S3.CredentialsSecretRef.Name, key))
		}
	}

	// Service must exist before the AC push so the DNS in maintainedMonarchComponents
	// resolves once the agent picks up the new AC.
	svc := construct.BuildMonarchService(rs, rs.Namespace)
	if _, err := controllerutil.CreateOrUpdate(ctx, reconciler.client, svc, func() error {
		fresh := construct.BuildMonarchService(rs, rs.Namespace)
		svc.Spec.Selector = fresh.Spec.Selector
		svc.Spec.Ports = fresh.Spec.Ports
		svc.Labels = fresh.Labels
		return nil
	}); err != nil {
		return workflow.Failed(xerrors.Errorf("failed to create/update Monarch Service %s: %w", svc.Name, err))
	}

	return workflow.OK()
}

// reconcileMonarchPostAC creates the ConfigMap and Deployment after the AC push
// has completed. For the active role it fetches the agent-API AC to extract
// cleartext mms-shipper credentials and embeds them into the shipper's URIs.
//
// If OM hasn't yet emitted the shipper user (race between AC push and OM's
// own user-provisioning step), this returns workflow.Pending. No Deployment
// exists yet, so there's nothing to crashloop.
func (r *ReplicaSetReconcilerHelper) reconcileMonarchPostAC(ctx context.Context, conn om.Connection, agentAPIKey string) workflow.Status {
	rs := r.resource
	reconciler := r.reconciler
	log := r.log

	role := rs.Spec.Monarch.Role
	conditionType := mdbv1.ConditionInjectorReady
	monarchRole := "injector"
	if role == mdbv1.MonarchRoleActive {
		conditionType = mdbv1.ConditionShipperReady
		monarchRole = "shipper"
	}

	srcURI := connectionstring.Builder().
		SetName(rs.Name).
		SetNamespace(rs.Namespace).
		SetReplicas(rs.Spec.Members).
		SetService(rs.ServiceName()).
		SetIsReplicaSet(true).
		SetIsTLSEnabled(rs.Spec.IsSecurityTLSConfigEnabled()).
		SetClusterDomain(rs.Spec.GetClusterDomain()).
		Build()

	// Fetch the agent-API AC. We need it for two things:
	//  1. (active only) cleartext mms-shipper credentials. OM creates this user
	//     with a minimal shipperRole when SCRAM is enabled. The agent normally
	//     injects these creds into backupMongoNodeURI at runtime, but with
	//     externallyManaged=true the agent skips lifecycle management — so the
	//     operator does the injection here.
	//  2. (both) the cluster keyfile (auth.key). The injector advertises itself
	//     to the standby's mongod as a sync source on port 9995; mongod
	//     heartbeats to it speak intra-cluster keyfile auth, so the injector
	//     binary needs --securityKeyFile pointing at the same key. Same applies
	//     to the shipper for symmetry — keyfile is harmless when unused.
	//
	// These fields only surface on the agent-API endpoint (not the public REST
	// AC, which returns SCRAM hashes). The pre-AC + AC-push step ensured
	// maintainedMonarchComponents is in the AC; on the next reconcile cycle OM
	// has usually emitted the user. If not, we requeue with no Deployment yet.
	ac, err := conn.ReadAgentAutomationConfig(agentAPIKey)
	if err != nil {
		return workflow.Pending("Waiting for OM AC fetch: %v", err)
	}

	// Look up cleartext mms-shipper credentials when present in the AC.
	// Two paths reach this code:
	//
	//   - Fresh active CR: the AC has shipperConfig (operator pushed it during
	//     pre-AC). OM emits mms-shipper after seeing it. We embed those creds
	//     in the shipper's URIs → shipper tails oplog and produces FCBIS.
	//   - Promoted CR (was standby): the AC still has injectorConfig — we do
	//     NOT push a new active-shape AC at promotion. OM never emits mms-shipper.
	//     The shipper starts with empty creds and cannot authenticate to mongod.
	//     This is accepted for PP: promoted clusters do not resume shipping.
	//     To restore DR, rebuild from scratch per the EA Standby Clusters playbook.
	mongodUser, mongodPassword := "", ""
	if role == mdbv1.MonarchRoleActive {
		for _, mc := range ac.GetMaintainedMonarchComponents() {
			if mc.ReplicaSetID == rs.Name && mc.ShipperConfig != nil {
				mongodUser = mc.ShipperConfig.ShipperUser
				mongodPassword = mc.ShipperConfig.ShipperPwd
				break
			}
		}
	}

	// Reconcile the Monarch secrets bundle — a single K8s Secret holding every
	// agent-managed file the Monarch pods need (today: just the cluster keyfile;
	// in the future also TLS member certs / CA bundles when we extend Monarch
	// to TLS-enabled clusters). Consolidating into one Secret keeps the volume
	// mount story simple: one Secret, one mountPath, multiple keys. Each new
	// file type is then ~10 lines here (extract from AC + add map key) plus a
	// matching `securityXFilePath:` line in the YAML config.
	//
	// Empty when SCRAM is off (no keyfile, no TLS yet) — downstream consumers
	// see secretName="" and skip the mount.
	monarchSecretsName := ""
	if ac.Auth != nil && ac.Auth.Key != "" {
		monarchSecretsName = fmt.Sprintf("%s-monarch-secrets", rs.Name)
		monarchSecrets := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            monarchSecretsName,
				Namespace:       rs.Namespace,
				OwnerReferences: kube.BaseOwnerReference(rs),
			},
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, reconciler.client, monarchSecrets, func() error {
			monarchSecrets.Type = corev1.SecretTypeOpaque
			if monarchSecrets.Data == nil {
				monarchSecrets.Data = map[string][]byte{}
			}
			monarchSecrets.Data["keyfile"] = []byte(ac.Auth.Key)
			// Future: monarchSecrets.Data["tls.pem"] = ac.AgentSSL.X
			//         monarchSecrets.Data["ca.crt"]  = ac.AgentSSL.Y
			return nil
		}); err != nil {
			return workflow.Failed(xerrors.Errorf("failed to create/update Monarch secrets bundle %s: %w", monarchSecretsName, err))
		}
	}

	configSecret := construct.BuildMonarchConfigSecret(rs, rs.Namespace, srcURI, mongodUser, mongodPassword, monarchSecretsName)
	if _, err := controllerutil.CreateOrUpdate(ctx, reconciler.client, configSecret, func() error {
		fresh := construct.BuildMonarchConfigSecret(rs, rs.Namespace, srcURI, mongodUser, mongodPassword, monarchSecretsName)
		configSecret.Data = fresh.Data
		configSecret.Labels = fresh.Labels
		return nil
	}); err != nil {
		return workflow.Failed(xerrors.Errorf("failed to create/update Monarch config Secret %s: %w", configSecret.Name, err))
	}

	// Hash triggers rolling restart when config changes (e.g. password rotation).
	configHash := monarchConfigHashFromSecret(configSecret.Data)

	dep := construct.BuildMonarchDeployment(rs, rs.Namespace, monarchSecretsName)
	if _, err := controllerutil.CreateOrUpdate(ctx, reconciler.client, dep, func() error {
		fresh := construct.BuildMonarchDeployment(rs, rs.Namespace, monarchSecretsName)
		dep.Spec = fresh.Spec
		dep.Labels = fresh.Labels
		if dep.Spec.Template.Annotations == nil {
			dep.Spec.Template.Annotations = map[string]string{}
		}
		dep.Spec.Template.Annotations["checksum/config"] = configHash
		return nil
	}); err != nil {
		apimeta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
			Type:    conditionType,
			Status:  metav1.ConditionFalse,
			Reason:  mdbv1.ReasonMonarchDeploymentFailed,
			Message: fmt.Sprintf("Failed to create/update Monarch Deployment: %v", err),
		})
		return workflow.Failed(xerrors.Errorf("failed to create/update Monarch Deployment %s: %w", dep.Name, err))
	}

	apimeta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
		Type:    conditionType,
		Status:  metav1.ConditionTrue,
		Reason:  mdbv1.ReasonMonarchDeploymentReady,
		Message: fmt.Sprintf("Monarch %s Deployment reconciled (role=%s)", monarchRole, role),
	})
	log.Infof("Reconciled Monarch %s K8s resources", monarchRole)

	// Note: post-promotion concerns (writing PromoteStandby to S3, waiting for
	// the agent) are owned by handleStandbyMonarchLifecycle at the top of
	// Reconcile, which intercepts before this code path runs. By the time we
	// reach this function the CR has no DR file in S3 — either a fresh active
	// CR or a fresh standby CR's first reconcile (auto-seed runs during preAC).
	return workflow.OK()
}

// swapMonarchToShipper performs the K8s-side swap from injector to shipper
// once S3 has reached Active. It is called from handlePromotedMonarchLifecycle
// and never pushes an automation config — the agent has already applied the
// active-shape AC server-side from its internal cache, and any operator-side
// AC mutation here would clobber that.
//
// Idempotent: re-runs every reconcile and converges to a no-op once the swap
// is complete (injector deleted, shipper ConfigMap + Deployment in place).
//
// Reuses reconcileMonarchPreAC and reconcileMonarchPostAC verbatim because they
// already encode the right behavior for an active CR: pre-AC deletes the injector
// resources and creates the shipper Service; post-AC fetches the OM agent-API AC
// to extract cleartext mms-shipper credentials, writes the keyfile Secret, and
// creates the ConfigMap + Deployment. The "no AC push" rule is preserved because
// the gate returns handled=true so the regular reconcile path (which would call
// updateOmDeploymentRs) is skipped entirely.
func (r *ReplicaSetReconcilerHelper) swapMonarchToShipper(ctx context.Context, agentAPIKey string, conn om.Connection) workflow.Status {
	if status := r.reconcileMonarchPreAC(ctx); !status.IsOK() {
		return status
	}
	return r.reconcileMonarchPostAC(ctx, conn, agentAPIKey)
}

// handlePromotedMonarchLifecycle owns the reconcile for a CR that has been
// flipped to spec.role=active AND has a DR-state file in S3 from when it was
// previously a standby. This is the only state where the operator must NOT
// run the regular provisioning path (which would push an active-shaped AC
// and reshape K8s infra — both forbidden by the EA Standby Clusters playbook
// for an existing DR-paired cluster).
//
// Returns (status, true) when this function owns the reconcile and the caller
// must surface `status` and stop. Returns (workflow.OK(), false) when normal
// provisioning should run: spec.role=standby (regular standby reconcile), or
// spec.role=active with no DR file (fresh active CR).
//
// State machine (spec.role=active && DR file present):
//
//	S3=Standby                                   → write PromoteStandby, Pending  (promotion trigger)
//	S3=PromoteStandby / StandbyReadyToPromote    → Pending (agent in flight)
//	S3=Active                                    → no-op  (promoted-and-done)
//	unexpected state                             → no-op  (defensive)
//
// Per the playbook (PP scope, unplanned failover only), the operator never
// reshapes the AC or K8s infra after a standby has been provisioned. The
// standby AC and injector Deployment written on the first reconcile stay for
// the CR's lifetime. To re-pair a promoted cluster the customer creates a
// new standby CR; the promoted-but-unshipping cluster does not auto-rewire.
func (r *ReplicaSetReconcilerHelper) handlePromotedMonarchLifecycle(ctx context.Context, conn om.Connection, agentAPIKey string) (workflow.Status, bool) {
	rs := r.resource
	log := r.log

	// Only active-spec CRs have anything to gate. Standby CRs always run the
	// regular reconcile path (which keeps InjectorReady fresh).
	if rs.Spec.Monarch == nil || rs.Spec.Monarch.Role == mdbv1.MonarchRoleStandby {
		return workflow.OK(), false
	}

	drClient, err := r.createDRStateClient(ctx)
	if err != nil {
		// reconcileMonarchS3State (regular path) will surface MonarchS3Unreachable.
		return workflow.OK(), false
	}

	current, err := drClient.Read(ctx)
	if err != nil {
		log.Warnw("Skipping promoted Monarch gate: cannot read S3 DR state", "error", err)
		return workflow.OK(), false
	}
	if current == nil {
		// No DR file → fresh active CR with no DR pair history.
		// Fall through to regular active provisioning.
		return workflow.OK(), false
	}

	// Mirror observed S3 state on status so kubectl describe surfaces it.
	now := metav1.Now()
	if rs.Status.Monarch == nil {
		rs.Status.Monarch = &mdbv1.MonarchStatus{}
	}
	rs.Status.Monarch.ObservedS3State = string(current.State)
	rs.Status.Monarch.ObservedS3StateTime = &now

	switch current.State {
	case drstate.StateActive:
		// Promoted-and-acknowledged: the customer flipped spec.role=active and
		// the agent has advanced S3 all the way to Active. Per the playbook, the
		// operator MUST NOT push a new AC at this point — the agent has already
		// applied the active-shape AC server-side from its internal cache, and
		// any operator-side AC push would clobber that. The only operator work
		// left is the K8s swap: tear down the injector, stand up the shipper.
		// swapMonarchToShipper is idempotent so it's a no-op once the swap has
		// converged on a steady-state promoted CR.
		return r.swapMonarchToShipper(ctx, agentAPIKey, conn), true

	case drstate.StateStandby:
		// Forward-only promotion trigger: customer flipped spec.role
		// standby → active. Stamp PromoteStandby on the DR file. The agent
		// drives the rest. Webhook rejects active → standby, so this is the
		// only direction we ever see.
		log.Info("Promotion triggered: spec.role=active, S3=Standby — writing PromoteStandby")
		if _, err := drClient.TransitionTo(ctx, drstate.StatePromoteStandby); err != nil {
			if stderrors.Is(err, drstate.ErrCASConflict) {
				return workflow.Pending("CAS conflict writing PromoteStandby, retrying"), true
			}
			return workflow.Pending("Failed to write PromoteStandby to S3, will retry: %v", err), true
		}
		return workflow.Pending("Promotion triggered: wrote PromoteStandby to S3, awaiting agent advance to Active"), true

	case drstate.StatePromoteStandby, drstate.StateStandbyReadyToPromote:
		// Agent in flight. Just wait.
		return workflow.Pending("Promotion in flight: S3 state is %s, awaiting agent advance to Active", current.State), true

	default:
		log.Warnw("Unexpected S3 state; leaving promoted Monarch alone", "s3State", current.State)
		return workflow.OK(), true
	}
}

// surfaceAgentPlanStatus reads OM's automation status and reflects any agent plan
// execution failures on the CR via ConditionAgentPlanStuck. The agent's readiness
// probe stays "ready" on stuck plans and the reconcile returns a generic
// "StatefulSet not ready" Pending — this condition gives users the real reason
// (e.g. a specific failing step on a specific process) directly on the CR.
//
// Best-effort: OM API errors are logged and ignored. Clears the condition when no
// failing process is seen.
func (r *ReplicaSetReconcilerHelper) surfaceAgentPlanStatus(ctx context.Context, conn om.Connection) {
	rs := r.resource
	log := r.log
	_ = ctx // future-proofing: OM client does not take ctx today

	as, err := conn.ReadAutomationStatus()
	if err != nil {
		log.Debugw("Could not read automation status for plan-error surfacing", "error", err)
		return
	}

	// Match processes belonging to this replica set. Process names for a replica set
	// are the pod names (e.g. "monarch-standby-rs-0"), so a prefix match against the
	// RS name filters correctly.
	prefix := rs.Name + "-"
	for _, p := range as.Processes {
		if p.Name != rs.Name && !strings.HasPrefix(p.Name, prefix) {
			continue
		}
		if p.HasPlanError() {
			apimeta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
				Type:    mdbv1.ConditionAgentPlanStuck,
				Status:  metav1.ConditionTrue,
				Reason:  mdbv1.ReasonAgentPlanError,
				Message: fmt.Sprintf("%s: %s", p.Name, p.ErrorString),
			})
			return
		}
	}
	apimeta.RemoveStatusCondition(&rs.Status.Conditions, mdbv1.ConditionAgentPlanStuck)
}

// buildMonarchComponentsForAC builds the maintainedMonarchComponents payload for
// the atomic AC push in updateOmDeploymentRs. Returns nil when spec.monarch is unset.
func (r *ReplicaSetReconcilerHelper) buildMonarchComponentsForAC(ctx context.Context) ([]om.MaintainedMonarchComponents, error) {
	rs := r.resource
	reconciler := r.reconciler
	if rs.Spec.Monarch == nil {
		return nil, nil
	}

	credSecret, err := reconciler.client.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Spec.Monarch.S3.CredentialsSecretRef.Name))
	if err != nil {
		return nil, xerrors.Errorf("failed to read Monarch credentials secret %s: %w", rs.Spec.Monarch.S3.CredentialsSecretRef.Name, err)
	}

	monarchRole := "injector"
	if rs.Spec.Monarch.Role == mdbv1.MonarchRoleActive {
		monarchRole = "shipper"
	}
	serviceDNS := construct.GetMonarchServiceDNS(rs.Name, monarchRole, rs.Namespace, rs.Spec.GetClusterDomain())
	srcURI := connectionstring.Builder().
		SetName(rs.Name).
		SetNamespace(rs.Namespace).
		SetReplicas(rs.Spec.Members).
		SetService(rs.ServiceName()).
		SetIsReplicaSet(true).
		SetIsTLSEnabled(rs.Spec.IsSecurityTLSConfigEnabled()).
		SetClusterDomain(rs.Spec.GetClusterDomain()).
		Build()

	return om.BuildMaintainedMonarchComponents(rs, rs.Name,
		string(credSecret.Data["awsAccessKeyId"]),
		string(credSecret.Data["awsSecretAccessKey"]),
		serviceDNS, srcURI)
}

// isInitialMonarchSetup returns true if Monarch is being added for the first time.
// This is derived from lastAchievedSpec (passive record of history), not tracked state.
// Used to skip agent waiting when agents can't reach goal without Monarch infra.
func isInitialMonarchSetup(rs *mdbv1.MongoDB, lastSpec *mdbv1.MongoDbSpec) bool {
	if rs.Spec.Monarch == nil {
		return false
	}
	return lastSpec == nil || lastSpec.Monarch == nil
}

// handleCASConflict logs and returns a pending status for S3 CAS conflicts.
func (r *ReplicaSetReconcilerHelper) handleCASConflict(operation string) workflow.Status {
	r.log.Warnf("CAS conflict %s, will retry", operation)
	return workflow.Pending("CAS conflict %s, retrying", operation)
}

// reconcileMonarchS3State reads the S3 DR state on every reconcile and handles unplanned failover.
// This addresses the case where an external tool (CLI) writes to S3 directly without updating the CR.
// Per K8s conventions:
// - S3 drives infrastructure (swap shipper/injector based on S3 state)
// - status.Monarch reflects observed S3 state
// - SpecOutOfSync condition warns when CR differs from S3
// - CR spec remains unchanged (user's declared intent is preserved)
//
// IMPORTANT: Only standby clusters monitor the S3 DR state file. Active clusters only have a shipper
// (no injector config), so the agent doesn't monitor S3 state. For unplanned failover, only the
// standby cluster needs to detect when an external tool writes PromoteStandby to S3.
// reconcileMonarchS3State observes the S3 DR state file on every reconcile and reflects
// it on the CR status. It does NOT drive provisioning — provisioning is purely from
// spec.Monarch.Role in reconcileMonarchResources. This function's job is informational:
//
//   - Update status.monarch.observedS3State so users can see the agent's view via kubectl
//   - Set MonarchS3Unreachable when S3 is unreadable (provisioning continues regardless)
//   - Set SpecOutOfSync when the CR's spec.monarch.role disagrees with what S3 implies
//
// Why no auto-infrastructure-swap on divergence: when a customer writes PromoteStandby to
// S3 directly (the EA CLI runbook), the agent advances S3 to Active. If the CR still says
// standby, the operator surfaces the drift and waits for the user to update spec.monarch.role.
// Updating the spec then drives the standard idempotent provisioning. This matches K8s
// convention — operators don't silently mutate user spec, and they don't override the
// declared desired state based on out-of-band actions.
//
// During the divergence window the cluster's mongod is serving writes (per the agent's RS
// reconfiguration) but no shipper is running, so oplogs aren't being uploaded to S3. The
// user is expected to acknowledge the promotion by updating the CR; that's a deliberate
// trade for clarity over silent convergence.
func (r *ReplicaSetReconcilerHelper) reconcileMonarchS3State(ctx context.Context) workflow.Status {
	rs := r.resource
	log := r.log

	// Active CRs do not interact with S3 from the operator side. The DR-state
	// file is owned by the standby. Promoted-from-standby CRs are intercepted
	// by handlePromotedMonarchLifecycle before this function runs (it returns
	// handled=true for any active CR with a DR file). So once we reach this
	// function with role=active, it's a fresh active CR with no DR pair: no-op.
	if rs.Spec.Monarch == nil || rs.Spec.Monarch.Role == mdbv1.MonarchRoleActive {
		return workflow.OK()
	}

	drClient, err := r.createDRStateClient(ctx)
	if err != nil {
		apimeta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
			Type:    mdbv1.ConditionMonarchS3Unreachable,
			Status:  metav1.ConditionTrue,
			Reason:  "ClientCreationFailed",
			Message: fmt.Sprintf("Cannot construct Monarch S3 client: %v", err),
		})
		log.Warnw("Failed to create DR state client, skipping S3 state check", "error", err)
		return workflow.OK() // Non-fatal: provisioning still proceeds from spec
	}

	s3State, err := drClient.Read(ctx)
	if err != nil {
		apimeta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
			Type:    mdbv1.ConditionMonarchS3Unreachable,
			Status:  metav1.ConditionTrue,
			Reason:  "ReadFailed",
			Message: fmt.Sprintf("Cannot read Monarch S3 DR state file: %v", err),
		})
		log.Warnw("Failed to read DR state from S3", "error", err)
		return workflow.OK()
	}

	apimeta.RemoveStatusCondition(&rs.Status.Conditions, mdbv1.ConditionMonarchS3Unreachable)

	now := metav1.Now()
	if rs.Status.Monarch == nil {
		rs.Status.Monarch = &mdbv1.MonarchStatus{}
	}
	if s3State != nil {
		rs.Status.Monarch.ObservedS3State = string(s3State.State)
		rs.Status.Monarch.ObservedS3StateTime = &now
	} else {
		rs.Status.Monarch.ObservedS3State = ""
	}

	// Auto-seed the S3 DR state file when absent. Standby-only — active CRs are
	// guarded out at the top of this function. TransitionTo on an absent file
	// writes from scratch (current==nil branch in drstate/drstate.go::TransitionTo).
	if s3State == nil {
		seedState := drstate.StateStandby
		log.Infow("S3 DR state file absent; auto-seeding Standby",
			"role", rs.Spec.Monarch.Role, "seedState", seedState)
		seeded, err := drClient.TransitionTo(ctx, seedState)
		if err != nil {
			apimeta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
				Type:    mdbv1.ConditionMonarchS3Unreachable,
				Status:  metav1.ConditionTrue,
				Reason:  "SeedFailed",
				Message: fmt.Sprintf("Failed to seed Monarch S3 DR state file: %v", err),
			})
			return workflow.Pending("Failed to seed S3 DR state, will retry: %v", err)
		}
		// Reflect the seed in the in-memory s3State so the rest of the function works
		// against the value we just wrote.
		s3State = &drstate.DRStateWithETag{DRState: seeded.DRState, ETag: seeded.ETag}
		rs.Status.Monarch.ObservedS3State = string(s3State.State)
		rs.Status.Monarch.ObservedS3StateTime = &now
	}
	apimeta.RemoveStatusCondition(&rs.Status.Conditions, mdbv1.ConditionMonarchS3StateRequired)

	// Only standby CRs reach this point (active CRs guarded at top). Surface
	// SpecOutOfSync when S3 says the cluster has been promoted (Active or
	// StandbyReadyToPromote) but spec.role is still standby — the customer
	// must flip spec.role to active so handlePromotedMonarchLifecycle can run
	// the K8s swap.
	s3ImpliesActive := s3State.State == drstate.StateActive || s3State.State == drstate.StateStandbyReadyToPromote
	if !s3ImpliesActive {
		apimeta.RemoveStatusCondition(&rs.Status.Conditions, mdbv1.ConditionSpecOutOfSync)
		return workflow.OK()
	}

	apimeta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
		Type:   mdbv1.ConditionSpecOutOfSync,
		Status: metav1.ConditionTrue,
		Reason: "S3StateMismatch",
		Message: fmt.Sprintf(
			"S3 DR state is '%s' but spec.monarch.role is 'standby'. The agent has promoted "+
				"this cluster; update spec.monarch.role to 'active' so the operator starts the shipper.",
			s3State.State),
	})
	log.Infow("CR/S3 divergence (informational, no action)",
		"s3State", s3State.State, "crRole", rs.Spec.Monarch.Role)
	return workflow.OK()
}

// createDRStateClient creates a DR state client for S3 coordination during failover.
func (r *ReplicaSetReconcilerHelper) createDRStateClient(ctx context.Context) (*drstate.Client, error) {
	rs := r.resource
	reconciler := r.reconciler
	s3Cfg := rs.Spec.Monarch.S3

	// Read AWS credentials from the secret
	credSecret, err := reconciler.client.GetSecret(ctx, kube.ObjectKey(rs.Namespace, s3Cfg.CredentialsSecretRef.Name))
	if err != nil {
		return nil, xerrors.Errorf("failed to read Monarch credentials secret %s: %w", s3Cfg.CredentialsSecretRef.Name, err)
	}

	awsKeyId := string(credSecret.Data["awsAccessKeyId"])
	awsSecret := string(credSecret.Data["awsSecretAccessKey"])

	if awsKeyId == "" || awsSecret == "" {
		return nil, xerrors.Errorf("Monarch credentials secret %s missing awsAccessKeyId or awsSecretAccessKey", s3Cfg.CredentialsSecretRef.Name)
	}

	cfg := drstate.ClientConfig{
		BucketName:      s3Cfg.Bucket,
		Region:          s3Cfg.Region,
		ClusterPrefix:   s3Cfg.GetPrefix(rs.Name),
		ClusterName:     rs.Name,
		Endpoint:        s3Cfg.Endpoint,
		PathStyleAccess: s3Cfg.PathStyle,
		AccessKeyID:     awsKeyId,
		SecretAccessKey: awsSecret,
	}

	return drstate.NewClient(ctx, cfg)
}

// deleteMonarchResourcesForRole deletes Deployment, Service, and ConfigMap for the specified role.
func (r *ReplicaSetReconcilerHelper) deleteMonarchResourcesForRole(ctx context.Context, role string) workflow.Status {
	rs := r.resource
	reconciler := r.reconciler
	log := r.log

	log.Infow("Deleting Monarch resources for role", "role", role)

	// Delete Deployment
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      construct.MonarchDeploymentName(rs.Name, role),
			Namespace: rs.Namespace,
		},
	}
	if err := reconciler.client.Delete(ctx, dep); err != nil && !k8serrors.IsNotFound(err) {
		return workflow.Failed(xerrors.Errorf("failed to delete Monarch %s Deployment: %w", role, err))
	}

	// Delete Service
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      construct.MonarchServiceName(rs.Name, role),
			Namespace: rs.Namespace,
		},
	}
	if err := reconciler.client.Delete(ctx, svc); err != nil && !k8serrors.IsNotFound(err) {
		return workflow.Failed(xerrors.Errorf("failed to delete Monarch %s Service: %w", role, err))
	}

	// Delete config Secret
	configSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      construct.MonarchConfigSecretName(rs.Name, role),
			Namespace: rs.Namespace,
		},
	}
	if err := reconciler.client.Delete(ctx, configSecret); err != nil && !k8serrors.IsNotFound(err) {
		return workflow.Failed(xerrors.Errorf("failed to delete Monarch %s config Secret: %w", role, err))
	}

	log.Infow("Deleted Monarch resources for role", "role", role)
	return workflow.OK()
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
	mutatedSts, err := create.DatabaseInKubernetes(ctx, reconciler.client, *rs, sts, rsConfig, log)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("failed to create/update (Kubernetes reconciliation phase): %w", err))
	}

	// Check StatefulSet status
	expectedGeneration := mutatedSts.GetGeneration()
	if status := statefulset.GetStatefulSetStatus(ctx, rs.Namespace, rs.Name, expectedGeneration, reconciler.client); !status.IsOK() {
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
	if architectures.IsRunningStaticArchitecture(rs.Annotations, reconciler.defaultArchitecture) {
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
		WithAgentImage(images.ContainerImage(reconciler.imageUrls, util.AgentImageUrlEnv, automationAgentVersion)),
		WithMongodbImage(images.GetOfficialImage(reconciler.imageUrls, rs.Spec.Version, rs.GetAnnotations(), reconciler.defaultArchitecture)),
		WithAgentDebug(reconciler.agentDebug),
		WithAgentDebugImage(reconciler.agentDebugImage),
		WithDefaultArchitecture(reconciler.defaultArchitecture),
	)

	return rsConfig, nil
}

// AddReplicaSetController creates a new MongoDbReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddReplicaSetController(ctx context.Context, mgr manager.Manager, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise, enableClusterMongoDBRoles, agentDebug bool, agentDebugImage string, defaultArchitecture architectures.DefaultArchitecture) error {
	// Create a new controller
	reconciler := newReplicaSetReconciler(ctx, mgr.GetClient(), imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, agentDebug, agentDebugImage, defaultArchitecture, om.NewOpsManagerConnection)
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

	// Watch Deployments for Monarch components
	err = c.Watch(
		source.Kind[client.Object](mgr.GetCache(), &appsv1.Deployment{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &mdbv1.MongoDB{}, handler.OnlyControllerOwner())))
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

	// Watch for MongoDBSearch resources that reference ReplicaSet MongoDB resources
	// Only enqueue reconciliation requests for ReplicaSet resources, not Standalone or ShardedCluster
	kubeClient := mgr.GetClient()
	err = c.Watch(source.Kind(mgr.GetCache(), &searchv1.MongoDBSearch{},
		handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, search *searchv1.MongoDBSearch) []reconcile.Request {
			sourceRef := search.GetMongoDBResourceRef()
			if sourceRef == nil {
				return []reconcile.Request{}
			}
			// Fetch the MongoDB resource to check its ResourceType
			mdb := &mdbv1.MongoDB{}
			if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: sourceRef.Namespace, Name: sourceRef.Name}, mdb); err != nil {
				// If we can't fetch the resource, don't enqueue (it might not exist or be a different type)
				return []reconcile.Request{}
			}
			// Only enqueue if this is a ReplicaSet resource
			if mdb.Spec.ResourceType != mdbv1.ReplicaSet {
				return []reconcile.Request{}
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: sourceRef.Namespace, Name: sourceRef.Name}}}
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

	replicaSet := replicaset.BuildFromMongoDBWithReplicas(reconciler.imageUrls[util.MongodbImageEnv], reconciler.forceEnterprise, rs, replicasTarget, rs.CalculateFeatureCompatibilityVersion(), tlsCertPath, reconciler.defaultArchitecture)

	// dbPath handling for Monarch.
	//
	// OM enforces: dbPath may not change for an existing process
	// (AutomationDiffValidationSvc.failIfDbPathChange). So we MUST never mutate
	// dbPath on a process that's already in OM. The operator's default in
	// process.go is "/data", which is the mount root in our pods.
	//
	// The agent's FCBIS DownloadFCBIS step (mms-automation
	// action/downloadoplogmigrationsnapshot/downloadoplogmigrationsnapshot_seeder.go::snapshotStagingDir)
	// stages snapshots as a sibling of dbPath, then atomic-renames onto it.
	// dbPath="/data" makes the sibling resolve under "/" (read-only container
	// root) and the atomic rename target a mount point — both fail. So fresh
	// Monarch processes need dbPath one level inside the mount (/data/db).
	//
	// Combined rule:
	//   - Process exists in OM with a known dbPath: preserve it. Mutating would
	//     trip OM's validator. This makes "active extension" (existing /data RS
	//     + spec.monarch.role: active) and "standby→active promotion" (process
	//     was provisioned at /data/db) both safe — neither mutates dbPath.
	//   - New process AND spec.monarch != nil: default to /data/db so FCBIS
	//     works on first standby provisioning.
	//   - New process AND spec.monarch == nil: leave the operator default ("/data")
	//     untouched.
	//
	// Reading from OM (vs LastAchievedSpec / annotations) is authoritative:
	// OM is the system that enforces the constraint, and its view is robust
	// against operator-local state drift (backup/restore of the CR, manual OM
	// edits, etc.). The cost is one extra ReadDeployment, which we already do
	// inside ReadUpdateDeployment below — we capture its result here.
	existingDbPaths := map[string]string{}
	if existingDeployment, err := conn.ReadDeployment(); err == nil {
		for _, p := range existingDeployment.ProcessesCopy() {
			if dp := p.DbPath(); dp != "" {
				existingDbPaths[p.Name()] = dp
			}
		}
	}
	for i := range replicaSet.Processes {
		name := replicaSet.Processes[i].Name()
		if existing, ok := existingDbPaths[name]; ok {
			replicaSet.Processes[i].SetDbPath(existing)
		} else if rs.Spec.Monarch != nil {
			replicaSet.Processes[i].SetDbPath("/data/db")
		}
	}

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

	// Build maintainedMonarchComponents so it can be pushed atomically with RS
	// processes in the single ReadUpdateDeployment below. Splitting these into
	// two AC writes lets agents initialize a regular RS between the writes,
	// which the mms-automation planner cannot reconfigure into standby mode.
	monarchMC, err := r.buildMonarchComponentsForAC(ctx)
	if err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			if shouldMirrorKeyfileForMongot {
				if err := r.mirrorKeyfileIntoSecretForMongot(ctx, d); err != nil {
					return err
				}
			}
			if err := ReconcileReplicaSetAC(ctx, d, rs.Spec.DbCommonSpec, lastRsConfig.ToMap(), rs.Name, replicaSet, caFilePath, internalClusterCertPath, &prometheusConfiguration, log); err != nil {
				return err
			}
			if monarchMC != nil {
				d.SetMaintainedMonarchComponents(monarchMC)
			}
			return nil
		},
		log,
	)

	if err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	// Skip waiting for agents during initial Monarch setup.
	// Agents can't reach goal state until injector/shipper Deployment exists,
	// which is created by reconcileMonarch (runs after this function).
	// On next reconcile, Monarch infra will exist and agents will reach goal.
	if isInitialMonarchSetup(rs, r.deploymentState.LastAchievedSpec) {
		log.Info("Skipping agent wait during initial Monarch setup - Deployment will be created next")
	} else {
		if err := om.WaitForReadyState(conn, processNames, isRecovering, log); err != nil {
			return workflow.Failed(err)
		}
	}

	reconcileResult, _ := ReconcileLogRotateSetting(conn, rs.Spec.Agent, log)
	if !reconcileResult.IsOK() {
		return reconcileResult
	}

	if additionalReconciliationRequired {
		return workflow.Pending("Performing multi stage reconciliation")
	}

	hostsBefore := getAllHostsForReplicas(rs, membersNumberBefore)
	hostsAfter := getAllHostsForReplicas(rs, scale.ReplicasThisReconciliation(rs))

	if err := host.CalculateDiffAndStopMonitoring(conn, hostsBefore, hostsAfter, log); err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	if status := reconciler.ensureBackupConfigurationAndUpdateStatus(ctx, conn, rs, reconciler.SecretClient, log, 0); !status.IsOK() && !isRecovering {
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
	conn, _, err := connection.PrepareOpsManagerConnection(ctx, r.reconciler.SecretClient, projectConfig, credsConfig, r.reconciler.omConnectionFactory, rs.Namespace, true, log)
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
			// Remove Monarch components so the agent stops trying to reach deleted endpoints.
			// Only needed when Monarch was configured on this CR.
			if rs.Spec.Monarch != nil {
				delete(d, "maintainedMonarchComponents")
			}

			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	// Collect errors during cleanup but continue with all cleanup steps.
	// This ensures we attempt all cleanup operations even if some fail.
	var errs error

	if err := om.WaitForReadyState(conn, processNames, false, log); err != nil {
		errs = multierror.Append(errs, xerrors.Errorf("failed to wait for ready state. Continuing with cleanup: %w", err))
	}

	if rs.Spec.Backup != nil && rs.Spec.Backup.AutoTerminateOnDeletion {
		if err := backup.StopBackupIfEnabled(conn, conn, rs.Name, backup.ReplicaSetType, log); err != nil {
			errs = multierror.Append(errs, xerrors.Errorf("failed to stop backup. Continuing with cleanup: %w", err))
		}
	}

	// During deletion, calculate the maximum number of hosts that could possibly exist to ensure complete cleanup.
	// Reading from Status here is appropriate since this is outside the reconciliation loop.
	hostsToRemove, _ := dns.GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain(), util.MaxInt(rs.Status.Members, rs.Spec.Members), rs.Spec.GetExternalDomain())
	log.Infow("Stop monitoring removed hosts in Ops Manager", "removedHosts", hostsToRemove)

	if err := host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		// StopMonitoring may fail with 401 if hosts are already removed or auth is misconfigured.
		errs = multierror.Append(errs, xerrors.Errorf("failed to stop monitoring for hosts %v. Continuing with cleanup: %w", hostsToRemove, err))
	}

	if err := r.reconciler.clearProjectAuthenticationSettings(ctx, conn, rs, processNames, log); err != nil {
		errs = multierror.Append(errs, xerrors.Errorf("failed to clear project authentication settings. Continuing with cleanup: %w", err))
	}

	log.Infow("Clear feature control for group: %s", "groupID", conn.GroupID())
	if result := controlledfeature.ClearFeatureControls(conn, conn.OpsManagerVersion(), log); !result.IsOK() {
		result.Log(log)
		log.Warnf("Failed to clear feature control from group: %s", conn.GroupID())
	}

	if errs != nil {
		log.Warnf("Replica set cleanup from Ops Manager completed with errors")
	} else {
		log.Info("Removed replica set from Ops Manager!")
	}
	return errs
}

func (r *ReconcileMongoDbReplicaSet) OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error {
	helper, err := r.newReconcilerHelper(ctx, obj.(*mdbv1.MongoDB), log)
	if err != nil {
		return err
	}
	return helper.OnDelete(ctx, obj, log)
}

func getAllHostsForReplicas(rs *mdbv1.MongoDB, membersCount int) []string {
	hostnames, _ := dns.GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain(), membersCount, rs.Spec.DbCommonSpec.GetExternalDomain())
	return hostnames
}

func (r *ReplicaSetReconcilerHelper) applySearchOverrides(ctx context.Context) (bool, error) {
	rs := r.resource
	log := r.log

	search, err := r.lookupCorrespondingSearchResource(ctx)
	if err != nil {
		return false, err
	}

	if search == nil {
		log.Debugf("No MongoDBSearch resource found, skipping search overrides")
		return false, nil
	}

	log.Infof("Applying search overrides from MongoDBSearch %s", search.NamespacedName())

	if rs.Spec.AdditionalMongodConfig == nil {
		rs.Spec.AdditionalMongodConfig = mdbv1.NewEmptyAdditionalMongodConfig()
	}
	searchClusterIndex := searchcontroller.ResolveSingleClusterIndex(search)
	searchMongodConfig := searchcontroller.GetMongodConfigParameters(search, rs.Spec.GetClusterDomain(), searchClusterIndex)
	rs.Spec.AdditionalMongodConfig.AddOption("setParameter", searchMongodConfig["setParameter"])

	return true, nil
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

func (r *ReplicaSetReconcilerHelper) lookupCorrespondingSearchResource(ctx context.Context) (*searchv1.MongoDBSearch, error) {
	rs := r.resource
	reconciler := r.reconciler

	var search *searchv1.MongoDBSearch
	searchList := &searchv1.MongoDBSearchList{}
	if err := reconciler.client.List(ctx, searchList, &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(searchv1.MongoDBSearchIndexFieldName, rs.GetNamespace()+"/"+rs.GetName()),
	}); err != nil {
		return nil, xerrors.Errorf("Failed to list MongoDBSearch resources referred in the MongoDB resource %s/%s. err : %v", rs.Namespace, rs.Name, err)
	}

	if len(searchList.Items) == 0 {
		return nil, nil
	}

	if len(searchList.Items) > 1 {
		return nil, xerrors.Errorf("Found multiple MongoDBSearch resources referred in sharded cluster %s/%s", rs.Namespace, rs.Name)
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
	return search, nil
}

// monarchConfigHashFromSecret returns a short SHA256 hash of the Secret data to use
// as a pod template annotation, triggering a rolling restart when config changes.
func monarchConfigHashFromSecret(data map[string][]byte) string {
	b, _ := json.Marshal(data)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}
