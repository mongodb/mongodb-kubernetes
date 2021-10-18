package operator

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/replicaset"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"

	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"
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
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
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
	agents.UpgradeAllIfNeeded(r.client, r.omConnectionFactory, GetWatchedNamespace())

	log := zap.S().With("ReplicaSet", request.NamespacedName)
	rs := &mdbv1.MongoDB{}

	if reconcileResult, err := r.prepareResourceForReconciliation(request, rs, log); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcileResult, err
	}

	log.Info("-> ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs.Spec, "desiredReplicas", scale.ReplicasThisReconciliation(rs), "isScaling", scale.IsStillScaling(rs))
	log.Infow("ReplicaSet.Status", "status", rs.Status)

	if err := rs.ProcessValidationsOnReconcile(nil); err != nil {
		return r.updateStatus(rs, workflow.Invalid(err.Error()), log)
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, rs, log)
	if err != nil {
		return r.updateStatus(rs, workflow.Failed(err.Error()), log)
	}

	conn, err := connection.PrepareOpsManagerConnection(r.client, projectConfig, credsConfig, r.omConnectionFactory, rs.Namespace, log)
	if err != nil {
		return r.updateStatus(rs, workflow.Failed("Failed to prepare Ops Manager connection: %s", err), log)
	}

	if status := ensureSupportedOpsManagerVersion(conn); status.Phase() != mdbstatus.PhaseRunning {
		return r.updateStatus(rs, status, log)
	}

	// We remove all watched resources
	r.RemoveDependentWatchedResources(rs.ObjectKey())

	// And then add the ones we care about
	r.RegisterWatchedMongodbResources(rs.ObjectKey(), rs.Spec.GetProject(), rs.Spec.Credentials)

	// And TLS if needed
	if rs.GetSecurity().IsTLSEnabled() {
		r.RegisterWatchedTLSResources(rs.ObjectKey(), rs.Spec.GetTLSConfig().CA, []string{rs.GetSecurity().MemberCertificateSecretName(rs.Name)})
	}

	reconcileResult := checkIfHasExcessProcesses(conn, rs, log)
	if !reconcileResult.IsOK() {
		return r.updateStatus(rs, reconcileResult, log)
	}

	if status := validateMongoDBResource(rs, conn); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	status, newTLSDesignMemberCert := certs.EnsureSSLCertsForStatefulSet(r.client, *rs.Spec.Security, certs.ReplicaSetConfig(*rs), log)
	if !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	if status := controlledfeature.EnsureFeatureControls(*rs, conn, conn.OpsManagerVersion(), log); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return r.updateStatus(rs, workflow.Failed(err.Error()), log)
	}

	status, newTLSDesignForCerts := r.ensureX509SecretAndCheckTLSType(rs, currentAgentAuthMode, getReplicaSetCertsOption, log)
	if !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	rsCertsConfig := certs.ReplicaSetConfig(*rs)

	rsConfig := construct.ReplicaSetOptions(
		PodEnvVars(newPodVars(conn, projectConfig, rs.Spec.ConnectionSpec)),
		CurrentAgentAuthMechanism(currentAgentAuthMode),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.client, rs.Namespace, rsCertsConfig.CertSecretName, log)),
		InternalClusterHash(enterprisepem.ReadHashFromSecret(r.client, rs.Namespace, rsCertsConfig.InternalClusterSecretName, log)),
		NewTLSDesignKey(rs.GetSecurity().MemberCertificateSecretName(rs.Name), newTLSDesignMemberCert),
		NewTLSDesignMap(newTLSDesignForCerts),
	)

	caFilePath := util.CAFilePathInContainer
	if newTLSDesignMemberCert {
		caFilePath = fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)
	}

	sts := construct.DatabaseStatefulSet(*rs, rsConfig)

	if status := ensureRoles(rs.Spec.GetSecurity().Roles, conn, log); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	if scale.ReplicasThisReconciliation(rs) < rs.Status.Members {
		if err := replicaset.PrepareScaleDownFromStatefulSet(conn, sts, rs, log); err != nil {
			return r.updateStatus(rs, workflow.Failed("Failed to prepare Replica Set for scaling down using Ops Manager: %s", err), log)
		}
	}

	rsStsOption := rsConfig(*rs)
	agentCertSecretName := rs.GetSecurity().AgentClientCertificateSecretName(rs.Name).Name
	if rsStsOption.CertSecretTypes.IsCertTLSType(agentCertSecretName) {
		agentCertSecretName += certs.OperatorGeneratedCertSuffix
	}

	status = workflow.RunInGivenOrder(needToPublishStateFirst(r.client, *rs, rsConfig, log),
		func() workflow.Status {
			return r.updateOmDeploymentRs(conn, rs.Status.Members, rs, sts, log, caFilePath, agentCertSecretName).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			if err := create.DatabaseInKubernetes(r.client, *rs, sts, construct.ReplicaSetOptions(), log); err != nil {
				return workflow.Failed("Failed to create/update (Kubernetes reconciliation phase): %s", err.Error())
			}

			if status := r.getStatefulSetStatus(rs.Namespace, rs.Name); !status.IsOK() {
				return status
			}
			_, _ = r.updateStatus(rs, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)

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

	log.Infof("Finished reconciliation for MongoDbReplicaSet! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return r.updateStatus(rs, workflow.OK(), log, mdbstatus.NewBaseUrlOption(deployment.Link(conn.BaseURL(), conn.GroupID())), mdbstatus.MembersOption(rs))
}

func getReplicaSetCertsOption(mdb mdbv1.MongoDB) []certs.Options {
	return []certs.Options{certs.ReplicaSetConfig(mdb)}
}

// AddReplicaSetController creates a new MongoDbReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddReplicaSetController(mgr manager.Manager) error {
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

	zap.S().Infof("Registered controller %s", util.MongoDbReplicaSetController)

	return nil
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (r *ReconcileMongoDbReplicaSet) updateOmDeploymentRs(conn om.Connection, membersNumberBefore int, rs *mdbv1.MongoDB,
	set appsv1.StatefulSet, log *zap.SugaredLogger, caFilePath string, agentCertSecretName string) workflow.Status {

	log.Debug("Entering UpdateOMDeployments")
	// Only "concrete" RS members should be observed
	// - if scaling down, let's observe only members that will remain after scale-down operation
	// - if scaling up, observe only current members, because new ones might not exist yet
	err := agents.WaitForRsAgentsToRegisterReplicasSpecified(set, util_int.Min(membersNumberBefore, int(*set.Spec.Replicas)), rs.Spec.GetClusterDomain(), conn, log)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	// If current operation is to Disable TLS, then we should the current members of the Replica Set,
	// this is, do not scale them up or down util TLS disabling has completed.
	shouldLockMembers, err := updateOmDeploymentDisableTLSConfiguration(conn, membersNumberBefore, rs, set, log, caFilePath)
	if err != nil {
		return workflow.Failed(err.Error())
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

	status, additionalReconciliationRequired := r.updateOmAuthentication(conn, processNames, rs, agentCertSecretName, caFilePath, log)
	if !status.IsOK() {
		return status
	}

	internalClusterPath := ""
	if hash, ok := set.Annotations[util.InternalCertAnnotationKey]; ok {
		internalClusterPath = fmt.Sprintf("%s%s", util.InternalClusterAuthMountPath, hash)
	}
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			// it is not possible to disable internal cluster authentication once enabled
			if d.ExistingProcessesHaveInternalClusterAuthentication(replicaSet.Processes) && rs.Spec.Security.GetInternalClusterAuthenticationMode() == "" {
				return fmt.Errorf("cannot disable x509 internal cluster authentication")
			}
			excessProcesses := d.GetNumberOfExcessProcesses(rs.Name)
			if excessProcesses > 0 {
				return fmt.Errorf("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)")
			}

			d.MergeReplicaSet(replicaSet, nil)
			d.AddMonitoringAndBackup(log, rs.Spec.GetTLSConfig().IsEnabled(), caFilePath)
			d.ConfigureTLS(rs.Spec.GetTLSConfig(), caFilePath)
			d.ConfigureInternalClusterAuthentication(processNames, rs.Spec.Security.GetInternalClusterAuthenticationMode(), internalClusterPath)
			return nil
		},
		log,
	)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return workflow.Failed(err.Error())
	}

	if additionalReconciliationRequired {
		return workflow.Pending("Performing multi stage reconciliation")
	}

	hostsBefore := getAllHostsRs(set, rs.Spec.GetClusterDomain(), membersNumberBefore)
	hostsAfter := getAllHostsRs(set, rs.Spec.GetClusterDomain(), scale.ReplicasThisReconciliation(rs))

	if err := host.CalculateDiffAndStopMonitoring(conn, hostsBefore, hostsAfter, log); err != nil {
		return workflow.Failed(err.Error())
	}

	if status := r.ensureBackupConfigurationAndUpdateStatus(conn, rs, log); !status.IsOK() {
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
			if !d.TLSConfigurationWillBeDisabled(rs.Spec.GetTLSConfig()) {
				return nil
			}

			tlsConfigWasDisabled = true
			d.ConfigureTLS(rs.Spec.GetTLSConfig(), caFilePath)

			// configure as much agents/Pods as we currently have, no more (in case
			// there's a scale up change at the same time).
			replicaSet := replicaset.BuildFromStatefulSetWithReplicas(set, rs.GetSpec(), membersNumberBefore)
			d.MergeReplicaSet(replicaSet, nil)

			return nil
		},
		log,
	)

	return tlsConfigWasDisabled, err
}

func (r *ReconcileMongoDbReplicaSet) OnDelete(obj runtime.Object, log *zap.SugaredLogger) error {
	rs := obj.(*mdbv1.MongoDB)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, rs, log)
	if err != nil {
		return err
	}

	log.Infow("Removing replica set from Ops Manager", "config", rs.Spec)
	conn, err := connection.PrepareOpsManagerConnection(r.client, projectConfig, credsConfig, r.omConnectionFactory, rs.Namespace, log)
	if err != nil {
		return err
	}
	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			processNames = d.GetProcessNames(om.ReplicaSet{}, rs.Name)
			// error means that replica set is not in the deployment - it's ok and we can proceed (could happen if
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

	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return err
	}

	hostsToRemove, _ := dns.GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain(), util.MaxInt(rs.Status.Members, rs.Spec.Members))
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

func getAllHostsRs(set appsv1.StatefulSet, clusterName string, membersCount int) []string {
	hostnames, _ := dns.GetDnsForStatefulSetReplicasSpecified(set, clusterName, membersCount)
	return hostnames
}
