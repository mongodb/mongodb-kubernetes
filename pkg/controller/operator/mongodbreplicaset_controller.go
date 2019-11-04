package operator

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
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
	rs := &mdbv1.MongoDB{}

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

	projectConfig, err := r.kubeHelper.readProjectConfig(request.Namespace, rs.Spec.GetProject())
	if err != nil {
		log.Infof("error reading project %s", err)
		return retry()
	}

	rs.Spec.SetParametersFromConfigMap(projectConfig)

	podVars := &PodVars{}
	conn, err := r.prepareConnection(request.NamespacedName, rs.Spec.ConnectionSpec, podVars, log)
	if err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to prepare Ops Manager connection: %s", err), log)
	}

	reconcileResult := checkIfCanProceedWithWarnings(conn, rs)
	if !reconcileResult.isOk() {
		return reconcileResult.updateStatus(rs, r.ReconcileCommonController, log)
	}

	// cannot have a non-tls deployment in an x509 environment
	authSpec := rs.Spec.Security.Authentication
	if authSpec.Enabled && util.ContainsString(authSpec.Modes, util.X509) && !rs.Spec.GetTLSConfig().Enabled {
		msg := "cannot have a non-tls deployment when x509 authentication is enabled"
		return r.updateStatusValidationFailure(rs, msg, log)
	}

	// validate horizon config
	if len(rs.Spec.Connectivity.ReplicaSetHorizons) > 0 {
		if !rs.Spec.Security.TLSConfig.Enabled {
			msg := "TLS must be enabled in order to set replica set horizons"
			return r.updateStatusValidationFailure(rs, msg, log)
		}

		if len(rs.Spec.Connectivity.ReplicaSetHorizons) != rs.Spec.Members {
			msg := "Number of horizons must be equal to number of members in replica set"
			return r.updateStatusValidationFailure(rs, msg, log)
		}
	}

	replicaBuilder := r.kubeHelper.NewStatefulSetHelper(rs).
		SetService(rs.ServiceName()).
		SetPodVars(podVars).
		SetLogger(log).
		SetTLS(rs.Spec.GetTLSConfig()).
		SetProjectConfig(*projectConfig).
		SetSecurity(rs.Spec.Security)

	if status := r.kubeHelper.ensureSSLCertsForStatefulSet(replicaBuilder, log); !status.isOk() {
		return status.updateStatus(rs, r.ReconcileCommonController, log)
	}

	if status := r.ensureX509(rs, replicaBuilder, log); !status.isOk() {
		return status.updateStatus(rs, r.ReconcileCommonController, log)
	}

	replicaSetObject := replicaBuilder.BuildStatefulSet()

	if rs.Spec.Members < rs.Status.Members {
		if err := prepareScaleDownReplicaSet(conn, replicaSetObject, rs.Status.Members, rs, log); err != nil {
			return r.updateStatusFailed(rs, fmt.Sprintf("Failed to prepare Replica Set for scaling down using Ops Manager: %s", err), log)
		}
	}

	status := runInGivenOrder(replicaBuilder.needToPublishStateFirst(log),
		func() reconcileStatus {
			return r.updateOmDeploymentRs(conn, rs.Status.Members, rs, replicaSetObject, log).onErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() reconcileStatus {
			if err := replicaBuilder.CreateOrUpdateInKubernetes(); err != nil {
				return failed("Failed to create/update (Kubernetes reconciliation phase): %s", err.Error())
			}

			if !r.kubeHelper.isStatefulSetUpdated(rs.Namespace, rs.Name, log) {
				return pending(fmt.Sprintf("MongoDB %s resource is reconciling", rs.Name))
			}

			log.Info("Updated statefulsets for replica set")
			return ok()
		})

	if !status.isOk() {
		return status.updateStatus(rs, r.ReconcileCommonController, log)
	}

	log.Infof("Finished reconciliation for MongoDbReplicaSet! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return reconcileResult.updateStatus(rs, r.ReconcileCommonController, log, DeploymentLink(conn.BaseURL(), conn.GroupID()))
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
	err = c.Watch(&source.Kind{Type: &mdbv1.MongoDB{}}, &eventHandler, predicatesFor(mdbv1.ReplicaSet))

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
func (r *ReconcileMongoDbReplicaSet) updateOmDeploymentRs(conn om.Connection, membersNumberBefore int, newResource *mdbv1.MongoDB,
	set *appsv1.StatefulSet, log *zap.SugaredLogger) reconcileStatus {

	err := waitForRsAgentsToRegister(set, newResource.Spec.ClusterName, conn, log)
	if err != nil {
		return failedErr(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return failedErr(err)
	}

	x509Config := getX509ConfigurationState(ac, newResource.Spec.Security.Authentication.Modes)
	if x509Config.shouldLog() {
		log.Infof("X509 configuration status %+v", x509Config)
	}

	replicaSet := buildReplicaSetFromStatefulSet(set, newResource, log)
	processNames := replicaSet.GetProcessNames()

	if x509Config.x509EnablingHasBeenRequested && x509Config.x509CanBeEnabledInOpsManager {
		if err := enableX509Authentication(conn, log); err != nil {
			return failedErr(err)
		}
		if err := om.WaitForReadyState(conn, processNames, log); err != nil {
			return failedErr(err)
		}
	}

	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			// it is not possible to disable internal cluster authentication once enabled
			if d.ExistingProcessesHaveInternalClusterAuthentication(replicaSet.Processes) && newResource.Spec.Security.Authentication.InternalCluster == "" {
				return fmt.Errorf("cannot disable x509 internal cluster authentication")
			}
			numberOfOtherMembers, belongsTo := d.EnsureOneClusterPerProjectShouldProceed(newResource.Name)
			if numberOfOtherMembers > 0 && !belongsTo {
				return fmt.Errorf("cannot create more than 1 MongoDB Cluster per project")
			}
			d.MergeReplicaSet(replicaSet, nil)
			d.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)
			d.ConfigureTLS(newResource.Spec.GetTLSConfig())
			processNames = d.GetProcessNames(om.ReplicaSet{}, replicaSet.Rs.Name())
			return nil

		},
		log,
	)
	if err != nil {
		return failedErr(err)
	}

	if x509Config.x509EnablingHasBeenRequested && !x509Config.x509CanBeEnabledInOpsManager { // we want to enable x509, but can't as we are also enabling TLS in the same operation
		// this prevents the validation error "Invalid config: No TLS mode changes are allowed when toggling auth. Detected the following change: REPLICA_SET parameter tlsMode/sslMode changed from null to requireTLS/requireSSL."
		if err := om.WaitForReadyState(conn, processNames, log); err != nil {
			return failedErr(err)
		}
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

	hostsToRemove, _ := GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.ClusterName, util.MaxInt(rs.Status.Members, rs.Spec.Members))
	log.Infow("Stop monitoring removed hosts in Ops Manager", "removedHosts", hostsToRemove)

	if err = om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	log.Info("Removed replica set from Ops Manager!")
	return nil
}

func (r *ReconcileCommonController) ensureX509(mdb *mdbv1.MongoDB, helper *StatefulSetHelper, log *zap.SugaredLogger) reconcileStatus {
	spec := mdb.Spec
	authEnabled := mdb.Spec.Security.Authentication.Enabled
	usingX509 := util.ContainsString(mdb.Spec.Security.Authentication.Modes, util.X509)
	if authEnabled && usingX509 {
		authModes := mdb.Spec.Security.Authentication.Modes
		useCustomCA := mdb.Spec.GetTLSConfig().CA != ""
		successful, err := r.ensureX509AgentCertsForMongoDBResource(authModes, useCustomCA, mdb.Namespace, log)
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

		if spec.Security.Authentication.InternalCluster == util.X509 {
			if success, err := r.ensureInternalClusterCerts(helper, log); err != nil {
				return failed("Failed ensuring internal cluster authentication certs %s", err)
			} else if !success {
				return pending("Not all internal cluster authentication certs have been approved by Kubernetes CA")
			}
		}
	}
	return ok()
}

func prepareScaleDownReplicaSet(omClient om.Connection, statefulSet *appsv1.StatefulSet, oldMembersCount int, new *mdbv1.MongoDB, log *zap.SugaredLogger) error {
	_, podNames := GetDnsForStatefulSetReplicasSpecified(statefulSet, new.Spec.ClusterName, oldMembersCount)
	podNames = podNames[new.Spec.Members:oldMembersCount]

	return prepareScaleDown(omClient, map[string][]string{new.Name: podNames}, log)
}

func getAllHostsRs(set *appsv1.StatefulSet, clusterName string, membersCount int) []string {
	hostnames, _ := GetDnsForStatefulSetReplicasSpecified(set, clusterName, membersCount)
	return hostnames
}
