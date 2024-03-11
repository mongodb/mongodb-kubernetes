package operator

import (
	"context"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/recovery"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"golang.org/x/xerrors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/replicaset"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"

	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	util_int "github.com/10gen/ops-manager-kubernetes/pkg/util/int"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault/vaultwatcher"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileMongoDbReplicaSet reconciles a MongoDB with a type of ReplicaSet
type ReconcileMongoDbReplicaSet struct {
	*ReconcileCommonController
	omConnectionFactory om.ConnectionFactory
}

var _ reconcile.Reconciler = &ReconcileMongoDbReplicaSet{}

func newReplicaSetReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *ReconcileMongoDbReplicaSet {
	return &ReconcileMongoDbReplicaSet{
		ReconcileCommonController: newReconcileCommonController(mgr),
		omConnectionFactory:       omFunc,
	}
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
	agents.UpgradeAllIfNeeded(agents.ClientSecret{Client: r.client, SecretClient: r.SecretClient}, r.omConnectionFactory, GetWatchedNamespace(), false)

	log := zap.S().With("ReplicaSet", request.NamespacedName)
	rs := &mdbv1.MongoDB{}

	if reconcileResult, err := r.prepareResourceForReconciliation(request, rs, log); err != nil {
		if errors.IsNotFound(err) {
			return workflow.Invalid("Object for reconciliation not found").ReconcileResult()
		}
		return reconcileResult, err
	}

	log.Info("-> ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs.Spec, "desiredReplicas", scale.ReplicasThisReconciliation(rs), "isScaling", scale.IsStillScaling(rs))
	log.Infow("ReplicaSet.Status", "status", rs.Status)

	if err := rs.ProcessValidationsOnReconcile(nil); err != nil {
		return r.updateStatus(rs, workflow.Invalid(err.Error()), log)
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, rs, log)
	if err != nil {
		return r.updateStatus(rs, workflow.Failed(err), log)
	}

	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, rs.Namespace, log)
	if err != nil {
		return r.updateStatus(rs, workflow.Failed(xerrors.Errorf("Failed to prepare Ops Manager connection: %w", err)), log)
	}

	if status := ensureSupportedOpsManagerVersion(conn); status.Phase() != mdbstatus.PhaseRunning {
		return r.updateStatus(rs, status, log)
	}

	r.SetupCommonWatchers(rs, nil, nil, rs.Name)

	reconcileResult := checkIfHasExcessProcesses(conn, rs, log)
	if !reconcileResult.IsOK() {
		return r.updateStatus(rs, reconcileResult, log)
	}

	if status := validateMongoDBResource(rs, conn); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	status := certs.EnsureSSLCertsForStatefulSet(r.SecretClient, r.SecretClient, *rs.Spec.Security, certs.ReplicaSetConfig(*rs), log)
	if !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	prometheusCertHash, err := certs.EnsureTLSCertsForPrometheus(r.SecretClient, rs.GetNamespace(), rs.GetPrometheus(), certs.Database, log)
	if err != nil {
		log.Infof("Could not generate certificates for Prometheus: %s", err)
		return r.updateStatus(rs, workflow.Pending(err.Error()), log)
	}

	if status := controlledfeature.EnsureFeatureControls(*rs, conn, conn.OpsManagerVersion(), log); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return r.updateStatus(rs, workflow.Failed(err), log)
	}

	certConfigurator := certs.ReplicaSetX509CertConfigurator{MongoDB: rs, SecretClient: r.SecretClient}
	status = r.ensureX509SecretAndCheckTLSType(certConfigurator, currentAgentAuthMode, log)
	if !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	rsCertsConfig := certs.ReplicaSetConfig(*rs)

	var vaultConfig vault.VaultConfiguration
	var databaseSecretPath string
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
		databaseSecretPath = r.VaultClient.DatabaseSecretPath()
	}

	rsConfig := construct.ReplicaSetOptions(
		PodEnvVars(newPodVars(conn, projectConfig, rs.Spec.ConnectionSpec)),
		CurrentAgentAuthMechanism(currentAgentAuthMode),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.SecretClient, rs.Namespace, rsCertsConfig.CertSecretName, databaseSecretPath, log)),
		InternalClusterHash(enterprisepem.ReadHashFromSecret(r.SecretClient, rs.Namespace, rsCertsConfig.InternalClusterSecretName, databaseSecretPath, log)),
		PrometheusTLSCertHash(prometheusCertHash),
		WithVaultConfig(vaultConfig),
		WithLabels(rs.Labels),
		WithAdditionalMongodConfig(rs.Spec.GetAdditionalMongodConfig()),
	)

	caFilePath := util.CAFilePathInContainer
	caFilePath = fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)

	if err := r.reconcileHostnameOverrideConfigMap(log, r.client, *rs); err != nil {
		return r.updateStatus(rs, workflow.Failed(xerrors.Errorf("Failed to reconcileHostnameOverrideConfigMap: %w", err)), log)
	}

	sts := construct.DatabaseStatefulSet(*rs, rsConfig, log)
	if status := ensureRoles(rs.Spec.GetSecurity().Roles, conn, log); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	if scale.ReplicasThisReconciliation(rs) < rs.Status.Members {
		if err := replicaset.PrepareScaleDownFromStatefulSet(conn, sts, rs, log); err != nil {
			return r.updateStatus(rs, workflow.Failed(xerrors.Errorf("Failed to prepare Replica Set for scaling down using Ops Manager: %w", err)), log)
		}
	}

	agentCertSecretName := rs.GetSecurity().AgentClientCertificateSecretName(rs.Name).Name
	agentCertSecretName += certs.OperatorGeneratedCertSuffix

	// Recovery prevents some deadlocks that can occur during reconciliation, e.g. the setting of an incorrect automation
	// configuration and a subsequent attempt to overwrite it later, the operator would be stuck in Pending phase.
	// See CLOUDP-189433 and CLOUDP-229222 for more details.
	if recovery.ShouldTriggerRecovery(rs.Status.Phase != mdbstatus.PhaseRunning, rs.Status.LastTransition) {
		log.Warnf("Triggering Automatic Recovery. The MongoDB resource %s/%s is in %s state since %s", rs.Namespace, rs.Name, rs.Status.Phase, rs.Status.LastTransition)
		automationConfigStatus := r.updateOmDeploymentRs(conn, rs.Status.Members, rs, sts, log, caFilePath, agentCertSecretName, prometheusCertHash, true).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		deploymentError := create.DatabaseInKubernetes(r.client, *rs, sts, construct.ReplicaSetOptions(), log)
		if deploymentError != nil {
			log.Errorf("Recovery failed because of deployment errors, %w", deploymentError)
		}
		if !automationConfigStatus.IsOK() {
			log.Errorf("Recovery failed because of Automation Config update errors, %v", automationConfigStatus)
		}
	}

	status = workflow.RunInGivenOrder(needToPublishStateFirst(r.client, *rs, rsConfig, log),
		func() workflow.Status {
			return r.updateOmDeploymentRs(conn, rs.Status.Members, rs, sts, log, caFilePath, agentCertSecretName, prometheusCertHash, false).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			if err := create.DatabaseInKubernetes(r.client, *rs, sts, construct.ReplicaSetOptions(), log); err != nil {
				return workflow.Failed(xerrors.Errorf("Failed to create/update (Kubernetes reconciliation phase): %w", err))
			}

			if status := getStatefulSetStatus(rs.Namespace, rs.Name, r.client); !status.IsOK() {
				return status
			}
			_, _ = r.updateStatus(rs, workflow.Pending("").WithResourcesNotReady([]mdbstatus.ResourceNotReady{}), log)

			log.Info("Updated StatefulSet for replica set")
			return workflow.OK()

		})

	if !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	if scale.IsStillScaling(rs) {
		return r.updateStatus(rs, workflow.Pending("Continuing scaling operation for ReplicaSet %s, desiredMembers=%d, currentMembers=%d", rs.ObjectKey(), rs.DesiredReplicas(), scale.ReplicasThisReconciliation(rs)), log,
			mdbstatus.MembersOption(rs))
	}

	annotationsToAdd, err := getAnnotationsForResource(rs)
	if err != nil {
		return r.updateStatus(rs, workflow.Failed(err), log)
	}

	if vault.IsVaultSecretBackend() {
		secrets := rs.GetSecretsMountedIntoDBPod()
		vaultMap := make(map[string]string)
		for _, s := range secrets {
			path := fmt.Sprintf("%s/%s/%s", r.VaultClient.DatabaseSecretMetadataPath(), rs.Namespace, s)
			vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		}
		path := fmt.Sprintf("%s/%s/%s", r.VaultClient.OperatorScretMetadataPath(), rs.Namespace, rs.Spec.Credentials)
		vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		for k, val := range vaultMap {
			annotationsToAdd[k] = val
		}
	}

	if err := annotations.SetAnnotations(rs, annotationsToAdd, r.client); err != nil {
		return r.updateStatus(rs, workflow.Failed(err), log)
	}

	log.Infof("Finished reconciliation for MongoDbReplicaSet! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return r.updateStatus(rs, workflow.OK(), log, mdbstatus.NewBaseUrlOption(deployment.Link(conn.BaseURL(), conn.GroupID())), mdbstatus.MembersOption(rs))
}

func getHostnameOverrideConfigMapForReplicaset(mdb mdbv1.MongoDB) corev1.ConfigMap {
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

func (r *ReconcileMongoDbReplicaSet) reconcileHostnameOverrideConfigMap(log *zap.SugaredLogger, getUpdateCreator configmap.GetUpdateCreator, mdb mdbv1.MongoDB) error {
	if mdb.Spec.DbCommonSpec.GetExternalDomain() == nil {
		return nil
	}

	cm := getHostnameOverrideConfigMapForReplicaset(mdb)
	err := configmap.CreateOrUpdate(getUpdateCreator, cm)
	if err != nil && !errors.IsAlreadyExists(err) {
		return xerrors.Errorf("failed to create configmap: %s, err: %w", cm.Name, err)
	}
	log.Infof("Successfully ensured configmap: %s", cm.Name)

	return nil
}

// AddReplicaSetController creates a new MongoDbReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddReplicaSetController(mgr manager.Manager, memberClustersMap map[string]cluster.Cluster) error {
	// Create a new controller
	reconciler := newReplicaSetReconciler(mgr, om.NewOpsManagerConnection)
	c, err := controller.New(util.MongoDbReplicaSetController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	// watch for changes to replica set MongoDB resources
	eventHandler := ResourceEventHandler{deleter: reconciler}
	// Watch for changes to primary resource MongoDbReplicaSet
	err = c.Watch(&source.Kind{Type: &mdbv1.MongoDB{}}, &eventHandler, watch.PredicatesForMongoDB(mdbv1.ReplicaSet))

	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &mdbv1.MongoDB{},
	}, watch.PredicatesForStatefulSet())
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}},
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.Secret, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	// if vault secret backend is enabled watch for Vault secret change and trigger reconcile
	if vault.IsVaultSecretBackend() {
		eventChannel := make(chan event.GenericEvent)
		go vaultwatcher.WatchSecretChangeForMDB(zap.S(), eventChannel, reconciler.client, reconciler.VaultClient, mdbv1.ReplicaSet)

		err = c.Watch(
			&source.Channel{Source: eventChannel},
			&handler.EnqueueRequestForObject{},
		)
		if err != nil {
			zap.S().Errorf("Failed to watch for vault secret changes: %w", err)
		}
	}
	zap.S().Infof("Registered controller %s", util.MongoDbReplicaSetController)

	return nil
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (r *ReconcileMongoDbReplicaSet) updateOmDeploymentRs(conn om.Connection, membersNumberBefore int, rs *mdbv1.MongoDB,
	set appsv1.StatefulSet, log *zap.SugaredLogger, caFilePath string, agentCertSecretName string, prometheusCertHash string, isRecovering bool) workflow.Status {

	log.Debug("Entering UpdateOMDeployments")
	// Only "concrete" RS members should be observed
	// - if scaling down, let's observe only members that will remain after scale-down operation
	// - if scaling up, observe only current members, because new ones might not exist yet
	err := agents.WaitForRsAgentsToRegister(set, util_int.Min(membersNumberBefore, int(*set.Spec.Replicas)), rs.Spec.GetClusterDomain(), conn, log, rs)
	if err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	// If current operation is to Disable TLS, then we should the current members of the Replica Set,
	// this is, do not scale them up or down util TLS disabling has completed.
	shouldLockMembers, err := updateOmDeploymentDisableTLSConfiguration(conn, membersNumberBefore, rs, set, log, caFilePath)
	if err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	var updatedMembers int
	if shouldLockMembers {
		// We should not add or remove members during this run, we'll wait for
		// TLS to be completely disabled first.
		updatedMembers = membersNumberBefore
	} else {
		updatedMembers = int(*set.Spec.Replicas)
	}

	replicaSet := replicaset.BuildFromStatefulSetWithReplicas(set, rs.GetSpec(), updatedMembers)
	processNames := replicaSet.GetProcessNames()

	internalClusterPath := ""
	if hash := set.Annotations[util.InternalCertAnnotationKey]; hash != "" {
		internalClusterPath = fmt.Sprintf("%s%s", util.InternalClusterAuthMountPath, hash)
	}

	status, additionalReconciliationRequired := r.updateOmAuthentication(conn, processNames, rs, agentCertSecretName, caFilePath, internalClusterPath, isRecovering, log)
	if !status.IsOK() && !isRecovering {
		return status
	}

	lastRsConfig, err := rs.GetLastAdditionalMongodConfigByType(mdbv1.ReplicaSetConfig)
	if err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	p := PrometheusConfiguration{
		prometheus:         rs.GetPrometheus(),
		conn:               conn,
		secretsClient:      r.SecretClient,
		namespace:          rs.GetNamespace(),
		prometheusCertHash: prometheusCertHash,
	}

	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			return ReconcileReplicaSetAC(d, replicaSet.Processes, rs.Spec.DbCommonSpec, lastRsConfig.ToMap(), rs.Name, replicaSet, caFilePath, internalClusterPath, &p, log)
		},
		log,
	)

	if err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	if err := om.WaitForReadyState(conn, processNames, isRecovering, log); err != nil {
		return workflow.Failed(err)
	}

	if additionalReconciliationRequired {
		return workflow.Pending("Performing multi stage reconciliation")
	}

	externalDomain := rs.Spec.DbCommonSpec.GetExternalDomain()
	hostsBefore := getAllHostsRs(set, rs.Spec.GetClusterDomain(), membersNumberBefore, externalDomain)
	hostsAfter := getAllHostsRs(set, rs.Spec.GetClusterDomain(), scale.ReplicasThisReconciliation(rs), externalDomain)

	if err := host.CalculateDiffAndStopMonitoring(conn, hostsBefore, hostsAfter, log); err != nil && !isRecovering {
		return workflow.Failed(err)
	}

	if status := r.ensureBackupConfigurationAndUpdateStatus(conn, rs, r.SecretClient, log); !status.IsOK() && !isRecovering {
		return status
	}

	log.Info("Updated Ops Manager for replica set")
	return workflow.OK()
}

// updateOmDeploymentDisableTLSConfiguration checks if TLS configuration needs
// to be disabled. In which case it will disable it and inform to the calling
// function.
func updateOmDeploymentDisableTLSConfiguration(conn om.Connection, membersNumberBefore int, rs *mdbv1.MongoDB, set appsv1.StatefulSet, log *zap.SugaredLogger, caFilePath string) (bool, error) {
	tlsConfigWasDisabled := false

	err := conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			if !d.TLSConfigurationWillBeDisabled(rs.Spec.GetSecurity()) {
				return nil
			}

			tlsConfigWasDisabled = true
			d.ConfigureTLS(rs.Spec.GetSecurity(), caFilePath)

			// configure as many agents/Pods as we currently have, no more (in case
			// there's a scale up change at the same time).
			replicaSet := replicaset.BuildFromStatefulSetWithReplicas(set, rs.GetSpec(), membersNumberBefore)

			lastConfig, err := rs.GetLastAdditionalMongodConfigByType(mdbv1.ReplicaSetConfig)
			if err != nil {
				return err
			}

			d.MergeReplicaSet(replicaSet, rs.Spec.AdditionalMongodConfig.ToMap(), lastConfig.ToMap(), nil)

			return nil
		},
		log,
	)

	return tlsConfigWasDisabled, err
}

func (r *ReconcileMongoDbReplicaSet) OnDelete(obj runtime.Object, log *zap.SugaredLogger) error {
	rs := obj.(*mdbv1.MongoDB)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, rs, log)
	if err != nil {
		return err
	}

	log.Infow("Removing replica set from Ops Manager", "config", rs.Spec)
	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, rs.Namespace, log)
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

	hostsToRemove, _ := dns.GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain(), util.MaxInt(rs.Status.Members, rs.Spec.Members), nil)
	log.Infow("Stop monitoring removed hosts in Ops Manager", "removedHosts", hostsToRemove)

	if err = host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	if err := r.clearProjectAuthenticationSettings(conn, rs, processNames, log); err != nil {
		return err
	}

	r.RemoveDependentWatchedResources(rs.ObjectKey())

	log.Info("Removed replica set from Ops Manager!")
	return nil
}

func getAllHostsRs(set appsv1.StatefulSet, clusterName string, membersCount int, externalDomain *string) []string {
	hostnames, _ := dns.GetDnsForStatefulSetReplicasSpecified(set, clusterName, membersCount, externalDomain)
	return hostnames
}
