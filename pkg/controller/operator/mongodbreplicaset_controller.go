package operator

import (
	"fmt"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"

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
	log := zap.S().With("ReplicaSet", request.NamespacedName)
	rs := &mongodb.MongoDB{}

	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatusFailed(rs, fmt.Sprintf("Failed to reconcile Mongodb Replica Set: %s", err), log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)
	if reconcileResult, err := r.prepareResourceForReconciliation(request, rs, log); reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Info("-> ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs.Spec)
	log.Infow("ReplicaSet.Status", "status", rs.Status)

	spec := rs.Spec
	podVars := &PodVars{}
	conn, err := r.prepareConnection(request.NamespacedName, spec.ConnectionSpec, podVars, log)
	if err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to prepare Ops Manager connection: %s", err), log)
	}

	projectConfig, err := r.kubeHelper.readProjectConfig(request.Namespace, spec.Project)
	if err != nil {
		log.Infof("error reading project %s", err)
		return retry()
	}

	shouldContinue, warnings := om.CheckIfCanProceedWithWarnings(conn, rs)
	if !shouldContinue {
		return r.updateStatusFailed(rs, "cannot create more than 1 MongoDB Cluster per project", log)
	}
	// TODO: We agreed on having the warnings set here. It is not the best place, but this code will not last
	// for long.
	rs.Status.Warnings = warnings

	// cannot have a non-tls deployment in an x509 environment
	if projectConfig.AuthMode == util.X509 && !rs.Spec.GetTLSConfig().Enabled {
		return r.updateStatusValidationFailure(rs, fmt.Sprintf("cannot have a non-tls deployment when x509 authentication is enabled"), log)
	}

	replicaBuilder := r.kubeHelper.NewStatefulSetHelper(rs).
		SetService(rs.ServiceName()).
		SetReplicas(rs.Spec.Members).
		SetPersistence(rs.Spec.Persistent).
		SetPodSpec(NewDefaultPodSpecWrapper(*rs.Spec.PodSpec)).
		SetPodVars(podVars).
		SetExposedExternally(rs.Spec.ExposedExternally).
		SetLogger(log).
		SetTLS(rs.Spec.GetTLSConfig()).
		SetClusterName(rs.Spec.ClusterName).
		SetProjectConfig(*projectConfig).
		SetSecurity(rs.Spec.Security)

	if status := r.kubeHelper.ensureSSLCertsForStatefulSet(replicaBuilder, log); !status.isOk() {
		return status.updateStatus(rs, r.ReconcileCommonController, log)
	}

	if status := r.ensureX509(rs, projectConfig, replicaBuilder, log); !status.isOk() {
		return status.updateStatus(rs, r.ReconcileCommonController, log)
	}

	replicaSetObject := replicaBuilder.BuildStatefulSet()

	if spec.Members < rs.Status.Members {
		if err := prepareScaleDownReplicaSet(conn, replicaSetObject, rs.Status.Members, rs, log); err != nil {
			return r.updateStatusFailed(rs, fmt.Sprintf("Failed to prepare Replica Set for scaling down using Ops Manager: %s", err), log)
		}
	}

	status := runInGivenOrder(replicaBuilder.needToPublishStateFirst(log),
		func() reconcileStatus {
			return r.updateOmDeploymentRs(conn, rs.Status.Members, rs, replicaSetObject, projectConfig, log)
		},
		func() reconcileStatus {
			if err := replicaBuilder.CreateOrUpdateInKubernetes(); err != nil {
				return failedErr(fmt.Errorf("Failed to create/update (Kubernetes reconciliation phase): %s.", err))
			}
			log.Info("Updated statefulsets for replica set")
			return ok()
		})

	if !status.isOk() {
		return status.updateStatus(rs, r.ReconcileCommonController, log)
	}

	log.Infof("Finished reconciliation for MongoDbReplicaSet! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return r.updateStatusSuccessful(rs, log, DeploymentLink(conn.BaseURL(), conn.GroupID()))
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
	err = c.Watch(&source.Kind{Type: &mongodb.MongoDB{}}, &eventHandler, predicatesFor(mongodb.ReplicaSet))

	if err != nil {
		return err
	}

	//	// Watch for changes to secondary resource Statefulsets and requeue the owner MongoDbStandalone
	//	// TODO CLOUDP-35240
	//	/*err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
	//		IsController: true,
	//		OwnerType:    &mongodb.MongoDbReplicaSet{},
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
		&ConfigMapAndSecretHandler{resourceType: ConfigMap, trackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&ConfigMapAndSecretHandler{resourceType: Secret, trackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbReplicaSetController)

	return nil
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (r *ReconcileMongoDbReplicaSet) updateOmDeploymentRs(conn om.Connection, membersNumberBefore int, newResource *mongodb.MongoDB,
	set *appsv1.StatefulSet, projectConfig *ProjectConfig, log *zap.SugaredLogger) reconcileStatus {

	err := waitForRsAgentsToRegister(set, newResource.Spec.ClusterName, conn, log)
	if err != nil {
		return failedErr(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return failedErr(err)
	}

	x509Config := getX509ConfigurationState(ac, projectConfig)
	if x509Config.shouldLog() {
		log.Info("X509 configuration status %+v", x509Config)
	}

	replicaSet := buildReplicaSetFromStatefulSet(set, newResource, log)
	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			// it is not possible to disable internal cluster authentication once enabled
			if d.ExistingProcessesHaveInternalClusterAuthentication(replicaSet.Processes) && newResource.Spec.Security.ClusterAuthMode == "" {
				return fmt.Errorf("cannot disable x509 internal cluster authentication")
			}
			d.MergeReplicaSet(replicaSet, nil)
			d.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)
			d.ConfigureTLS(newResource.Spec.GetTLSConfig())
			processNames = d.GetProcessNames(om.ReplicaSet{}, replicaSet.Rs.Name())

			if x509Config.x509EnablingHasBeenRequested && x509Config.x509CanBeEnabledInOpsManager {
				return enableX509Authentication(d, conn, log)
			}
			return nil
		},
		getMutex(conn.GroupName(), conn.OrgID()),
		log,
	)
	if err != nil {
		return failedErr(err)
	}

	if x509Config.x509EnablingHasBeenRequested && !x509Config.x509CanBeEnabledInOpsManager { // we want to enable x509, but can't as we are also enabling TLS in the same operation
		// this prevents the validation error "Invalid config: No TLS mode changes are allowed when toggling auth. Detected the following change: REPLICA_SET parameter tlsMode/sslMode changed from null to requireTLS/requireSSL."
		return pending("Performing multi stage reconciliation")
	}

	if x509Config.shouldDisableX509 {
		if err := disableX509Authentication(conn, log); err != nil {
			return failedErr(err)
		}
	}

	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return failedErr(err)
	}

	hostsBefore := getAllHostsRs(set, newResource.Spec.ClusterName, membersNumberBefore)
	hostsAfter := getAllHostsRs(set, newResource.Spec.ClusterName, newResource.Spec.Members)
	if err := calculateDiffAndStopMonitoringHosts(conn, hostsBefore, hostsAfter, log); err != nil {
		return failedErr(err)
	}
	log.Info("Updated Ops Manager for replica set")
	return ok()
}

func (r *ReconcileMongoDbReplicaSet) delete(obj interface{}, log *zap.SugaredLogger) error {
	rs := obj.(*mongodb.MongoDB)

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
		getMutex(conn.GroupName(), conn.OrgID()),
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

	hostsToRemove, _ := GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.ClusterName, util.MaxInt(rs.Status.Members, rs.Spec.Members))
	log.Infow("Stop monitoring removed hosts in Ops Manager", "removedHosts", hostsToRemove)

	if err = om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	log.Info("Removed replica set from Ops Manager!")
	return nil
}

func (r *ReconcileCommonController) ensureX509(mdb *mongodb.MongoDB, projectConfig *ProjectConfig, helper *StatefulSetHelper, log *zap.SugaredLogger) reconcileStatus {
	spec := mdb.Spec
	if projectConfig.AuthMode == util.X509 {
		successful, err := r.ensureX509AgentCertsForMongoDBResource(projectConfig, mdb.Namespace)
		if err != nil {
			return failedErr(err)
		}
		if !successful {
			return pending("Agent certs have not yet been approved")
		}

		if !spec.Security.TLSConfig.Enabled {
			return failed("Authentication mode for project is x509 but this MDB resource is not TLS enabled")
		} else if !r.doAgentX509CertsExist(mdb.Namespace) {
			return pending("Agent x509 certificates have not yet been created")
		}

		if spec.Security.ClusterAuthMode == util.X509 {
			if success, err := r.ensureInternalClusterCerts(helper, log); err != nil {
				return failed("Failed ensuring internal cluster authentication certs %s", err)
			} else if !success {
				return pending("Not all internal cluster authentication certs have been approved by Kubernetes CA")
			}
		}
	} else {
		// this means the user has disabled x509 at the project level, but the resource is still configured to use x509 cluster authentication
		// as we don't have a status on the ConfigMap, we can inform the user in the status of the resource.
		if spec.Security.ClusterAuthMode == util.X509 {
			return failed("This deployment has clusterAuthenticationMode set to x509, ensure the ConfigMap for this project is configured to enable x509")
		}
	}
	return ok()
}

func prepareScaleDownReplicaSet(omClient om.Connection, statefulSet *appsv1.StatefulSet, oldMembersCount int, new *mongodb.MongoDB, log *zap.SugaredLogger) error {
	_, podNames := GetDnsForStatefulSetReplicasSpecified(statefulSet, new.Spec.ClusterName, oldMembersCount)
	podNames = podNames[new.Spec.Members:oldMembersCount]

	return prepareScaleDown(omClient, map[string][]string{new.Name: podNames}, log)
}

func getAllHostsRs(set *appsv1.StatefulSet, clusterName string, membersCount int) []string {
	hostnames, _ := GetDnsForStatefulSetReplicasSpecified(set, clusterName, membersCount)
	return hostnames
}
