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
		func() (reconcile.Result, error) {
			return r.updateStatusFailed(rs, "Failed to reconcile Mongodb Replica Set", log)
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
	conn, err := r.prepareConnection(request.NamespacedName, spec, podVars, log)
	if err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to prepare Ops Manager connection: %s", err), log)
	}

	replicaBuilder := r.kubeHelper.NewStatefulSetHelper(rs).
		SetService(rs.ServiceName()).
		SetReplicas(rs.Spec.Members).
		SetPersistence(rs.Spec.Persistent).
		SetPodSpec(NewDefaultPodSpecWrapper(*rs.Spec.PodSpec)).
		SetPodVars(podVars).
		SetExposedExternally(true).
		SetLogger(log)
	replicaSetObject := replicaBuilder.BuildStatefulSet()

	if spec.Members < rs.Status.Members {
		if err := prepareScaleDownReplicaSet(conn, replicaSetObject, rs.Status.Members, rs, log); err != nil {
			return r.updateStatusFailed(rs, fmt.Sprintf("Failed to prepare Replica Set for scaling down using Ops Manager: %s", err), log)
		}
	}

	err = replicaBuilder.CreateOrUpdateInKubernetes()
	if err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to create/update the StatefulSet: %s", err), log)
	}

	log.Info("Updated statefulset for replica set")

	if err := r.updateOmDeploymentRs(conn, rs.Status.Members, rs, replicaSetObject, log); err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to create/update replica set in Ops Manager: %s", err), log)
	}

	r.updateStatusSuccessful(rs, log, conn.BaseURL(), conn.GroupID())
	log.Infof("Finished reconciliation for MongoDbReplicaSet! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return reconcile.Result{}, nil
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
func (r *ReconcileMongoDbReplicaSet) updateOmDeploymentRs(conn om.Connection, membersNumberBefore int, new *mongodb.MongoDB,
	set *appsv1.StatefulSet, log *zap.SugaredLogger) error {

	err := waitForRsAgentsToRegister(set, new.Spec.ClusterName, conn, log)
	if err != nil {
		return err
	}
	replicaSet := buildReplicaSetFromStatefulSet(set, new.Spec.ClusterName, new.Spec.Version)

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			d.MergeReplicaSet(replicaSet, nil)

			d.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)

			processNames = d.GetProcessNames(om.ReplicaSet{}, replicaSet.Rs.Name())
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

	return calculateDiffAndStopMonitoringHosts(conn, getAllHostsRs(set, new, membersNumberBefore), getAllHostsRs(set, new, new.Spec.Members), log)

}

func (r *ReconcileMongoDbReplicaSet) delete(obj interface{}, log *zap.SugaredLogger) error {
	rs := obj.(*mongodb.MongoDB)

	log.Infow("Removing replica set from Ops Manager", "config", rs.Spec)
	conn, err := r.prepareConnection(objectKey(rs.Namespace, rs.Name), rs.Spec, nil, log)
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

	hostsToRemove, _ := GetDnsNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.ClusterName, util.MaxInt(rs.Status.Members, rs.Spec.Members))
	log.Infow("Stop monitoring removed hosts in Ops Manager", "removedHosts", hostsToRemove)

	if err = om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	log.Info("Removed replica set from Ops Manager!")
	return nil
}

func prepareScaleDownReplicaSet(omClient om.Connection, statefulSet *appsv1.StatefulSet, oldMembersCount int, new *mongodb.MongoDB, log *zap.SugaredLogger) error {
	_, podNames := GetDnsForStatefulSetReplicasSpecified(statefulSet, new.Spec.ClusterName, oldMembersCount)
	podNames = podNames[new.Spec.Members:oldMembersCount]

	return prepareScaleDown(omClient, map[string][]string{new.Name: podNames}, log)
}

func getAllHostsRs(set *appsv1.StatefulSet, rs *mongodb.MongoDB, membersCount int) []string {
	if rs == nil {
		return []string{}
	}

	hostnames, _ := GetDnsForStatefulSetReplicasSpecified(set, rs.Spec.ClusterName, membersCount)
	return hostnames
}
