package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/host"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/project"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileMongoDbReplicaSet reconciles a MongoDB with a type of ReplicaSet
type ReconcileMongoDbReplicaSet struct {
	*ReconcileCommonController
}

var _ reconcile.Reconciler = &ReconcileMongoDbReplicaSet{}

func newReplicaSetReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *ReconcileMongoDbReplicaSet {
	return &ReconcileMongoDbReplicaSet{newReconcileCommonController(mgr, omFunc)}
}

// Reconcile reads that state of the cluster for a MongoDbReplicaSet object and makes changes based on the state read
// and what is in the MongoDbReplicaSet.Spec
func (r *ReconcileMongoDbReplicaSet) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	agents.UpgradeAllIfNeeded(r.client, r.omConnectionFactory, getWatchedNamespace())

	log := zap.S().With("ReplicaSet", request.NamespacedName)
	rs := &mdbv1.MongoDB{}

	mutex := r.GetMutex(request.NamespacedName)
	mutex.Lock()
	defer mutex.Unlock()

	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatus(rs, workflow.Failed("Failed to reconcile Mongodb Replica Set: %s", err), log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)
	if reconcileResult, err := r.prepareResourceForReconciliation(request, rs, log); reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Info("-> ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs.Spec)
	log.Infow("ReplicaSet.Status", "status", rs.Status)

	if err := rs.ProcessValidationsOnReconcile(); err != nil {
		return r.updateStatus(rs, workflow.Invalid(err.Error()), log)
	}

	projectConfig, err := project.ReadProjectConfig(r.client, objectKey(request.Namespace, rs.Spec.GetProject()), rs.Name)
	if err != nil {
		log.Infof("error reading project %s", err)
		return retry()
	}

	podVars := &PodVars{}
	conn, err := r.prepareConnection(request.NamespacedName, rs.Spec.ConnectionSpec, podVars, log)
	if err != nil {
		return r.updateStatus(rs, workflow.Failed("Failed to prepare Ops Manager connection: %s", err), log)
	}

	reconcileResult := checkIfHasExcessProcesses(conn, rs, log)
	if !reconcileResult.IsOK() {
		return r.updateStatus(rs, reconcileResult, log)
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return r.updateStatus(rs, workflow.Failed(err.Error()), log)
	}

	replicaBuilder := r.kubeHelper.NewStatefulSetHelper(rs).
		SetService(rs.ServiceName()).
		SetPodVars(podVars).
		SetLogger(log).
		SetTLS(rs.Spec.GetTLSConfig()).
		SetProjectConfig(projectConfig).
		SetSecurity(rs.Spec.Security).
		SetReplicaSetHorizons(rs.Spec.Connectivity.ReplicaSetHorizons).
		SetCurrentAgentAuthMechanism(currentAgentAuthMode).
		SetStatefulSetConfiguration(nil) // TODO: configure once supported
	//SetStatefulSetConfiguration(rs.Spec.StatefulSetConfiguration)

	replicaBuilder.SetCertificateHash(replicaBuilder.readPemHashFromSecret())

	if status := validateMongoDBResource(rs, conn); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	if status := r.kubeHelper.ensureSSLCertsForStatefulSet(replicaBuilder, log); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	if status := r.ensureFeatureControls(rs, conn, log); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	if status := r.ensureX509InKubernetes(rs, replicaBuilder, log); !status.IsOK() {
		return r.updateStatus(rs, status, log)
	}

	replicaSetObject, err := replicaBuilder.BuildStatefulSet()
	if err != nil {
		return r.updateStatus(rs, workflow.Failed(err.Error()), log)
	}

	if rs.Spec.Members < rs.Status.Members {
		if err := prepareScaleDownReplicaSet(conn, replicaSetObject, rs.Status.Members, rs, log); err != nil {
			return r.updateStatus(rs, workflow.Failed("Failed to prepare Replica Set for scaling down using Ops Manager: %s", err), log)
		}
	}

	status := runInGivenOrder(replicaBuilder.needToPublishStateFirst(log),
		func() workflow.Status {
			return r.updateOmDeploymentRs(conn, rs.Status.Members, rs, replicaSetObject, log).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			if err := replicaBuilder.CreateOrUpdateInKubernetes(); err != nil {
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

	log.Infof("Finished reconciliation for MongoDbReplicaSet! %s", completionMessage(conn.BaseURL(), conn.GroupID()))

	return r.updateStatus(rs, workflow.OK(), log, mdbstatus.NewBaseUrlOption(DeploymentLink(conn.BaseURL(), conn.GroupID())))
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
	eventHandler := MongoDBResourceEventHandler{reconciler: reconciler}
	// Watch for changes to primary resource MongoDbReplicaSet
	err = c.Watch(&source.Kind{Type: &mdbv1.MongoDB{}}, &eventHandler, watch.PredicatesForMongoDB(mdbv1.ReplicaSet))

	if err != nil {
		return err
	}

	//	// Watch for changes to secondary resource Statefulsets and requeue the owner MongoDbStandalone
	//	// TODO CLOUDP-35240
	//	/*err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
	//		IsController: true,
	//		OwnerType:    &mdbv1.MongoDbReplicaSet{},
	//	}, predicate.Funcs{
	//		CreateFunc: func(e event.CreateEvent) bool {
	//			return false
	//		},
	//		UpdateFunc: func(e event.UpdateEvent) bool {
	//			// The controller must watch only for changes in spec made by users, we don't care about status changes
	//			if !reflect.DeepEqual(e.ObjectOld.(*appsv1.StatefulSet).Spec, e.ObjectNew.(*appsv1.StatefulSet).Spec) {
	//				return true
	//			}
	//			return false
	//		}})
	//	if err != nil {
	//		return err
	//	}*/
	//
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}},
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, TrackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.Secret, TrackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbReplicaSetController)

	return nil
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (r *ReconcileMongoDbReplicaSet) updateOmDeploymentRs(conn om.Connection, membersNumberBefore int, rs *mdbv1.MongoDB,
	set appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {

	err := waitForRsAgentsToRegister(set, rs.Spec.GetClusterDomain(), conn, log)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	replicaSet := buildReplicaSetFromStatefulSet(set, rs)
	processNames := replicaSet.GetProcessNames()

	status, additionalReconciliationRequired := r.updateOmAuthentication(conn, processNames, rs, log)
	if !status.IsOK() {
		return status
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
			d.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)
			d.ConfigureTLS(rs.Spec.GetTLSConfig())
			d.ConfigureInternalClusterAuthentication(processNames, rs.Spec.Security.GetInternalClusterAuthenticationMode())
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
	hostsAfter := getAllHostsRs(set, rs.Spec.GetClusterDomain(), rs.Spec.Members)
	if err := calculateDiffAndStopMonitoringHosts(conn, hostsBefore, hostsAfter, log); err != nil {
		return workflow.Failed(err.Error())
	}

	log.Info("Updated Ops Manager for replica set")
	return workflow.OK()
}

func (r *ReconcileMongoDbReplicaSet) delete(obj interface{}, log *zap.SugaredLogger) error {
	rs := obj.(*mdbv1.MongoDB)

	log.Infow("Removing replica set from Ops Manager", "config", rs.Spec)
	conn, err := r.prepareConnection(objectKey(rs.Namespace, rs.Name), rs.Spec.ConnectionSpec, nil, log)
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

	if err := om.StopBackupIfEnabled(conn, rs.Name, om.ReplicaSetType, log); err != nil {
		return err
	}

	hostsToRemove, _ := util.GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain(), util.MaxInt(rs.Status.Members, rs.Spec.Members))
	log.Infow("Stop monitoring removed hosts in Ops Manager", "removedHosts", hostsToRemove)

	if err = host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	log.Info("Removed replica set from Ops Manager!")
	return nil
}

func (r *ReconcileCommonController) ensureX509InKubernetes(mdb *mdbv1.MongoDB, helper *StatefulSetHelper, log *zap.SugaredLogger) workflow.Status {
	authSpec := mdb.Spec.Security.Authentication
	if authSpec == nil || !mdb.Spec.Security.Authentication.Enabled {
		return workflow.OK()
	}
	useCustomCA := mdb.Spec.GetTLSConfig().CA != ""
	if mdb.Spec.Security.ShouldUseX509(helper.CurrentAgentAuthMechanism) {
		successful, err := r.ensureX509AgentCertsForMongoDBResource(mdb, useCustomCA, mdb.Namespace, log)
		if err != nil {
			return workflow.Failed(err.Error())
		}
		if !successful {
			return workflow.Pending("Agent certs have not yet been approved")
		}

		if !mdb.Spec.Security.TLSConfig.Enabled {
			return workflow.Failed("Authentication mode for project is x509 but this MDB resource is not TLS enabled")
		} else if !r.doAgentX509CertsExist(mdb.Namespace) {
			return workflow.Pending("Agent x509 certificates have not yet been created")
		}
	}

	if mdb.Spec.Security.GetInternalClusterAuthenticationMode() == util.X509 {
		if success, err := r.ensureInternalClusterCerts(helper, log); err != nil {
			return workflow.Failed("Failed ensuring internal cluster authentication certs %s", err)
		} else if !success {
			return workflow.Pending("Not all internal cluster authentication certs have been approved by Kubernetes CA")
		}
	}
	return workflow.OK()
}

func prepareScaleDownReplicaSet(omClient om.Connection, statefulSet appsv1.StatefulSet, oldMembersCount int, new *mdbv1.MongoDB, log *zap.SugaredLogger) error {
	_, podNames := util.GetDnsForStatefulSetReplicasSpecified(statefulSet, new.Spec.GetClusterDomain(), oldMembersCount)
	podNames = podNames[new.Spec.Members:oldMembersCount]

	return prepareScaleDown(omClient, map[string][]string{new.Name: podNames}, log)
}

func getAllHostsRs(set appsv1.StatefulSet, clusterName string, membersCount int) []string {
	hostnames, _ := util.GetDnsForStatefulSetReplicasSpecified(set, clusterName, membersCount)
	return hostnames
}
