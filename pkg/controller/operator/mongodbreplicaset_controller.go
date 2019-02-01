package operator

import (
	"fmt"
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// ReconcileMongoDbReplicaSet reconciles a MongoDbReplicaSet object
type ReconcileMongoDbReplicaSet struct {
	*ReconcileCommonController
}

var _ reconcile.Reconciler = &ReconcileMongoDbReplicaSet{}

func newReplicaSetReconciler(mgr manager.Manager, omFunc om.ConnectionFunc) *ReconcileMongoDbReplicaSet {
	return &ReconcileMongoDbReplicaSet{newReconcileCommonController(mgr, omFunc)}
}

// Reconcile reads that state of the cluster for a MongoDbReplicaSet object and makes changes based on the state read
// and what is in the MongoDbReplicaSet.Spec
func (r *ReconcileMongoDbReplicaSet) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("ReplicaSet", request.NamespacedName)
	rs := &mongodb.MongoDbReplicaSet{}

	defer exceptionHandling(
		func() (reconcile.Result, error) {
			return r.updateStatusFailed(rs, "Failed to reconcile Mongodb Replica Set", log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	reconcileResult, err := r.prepareResourceForReconciliation(request, rs, log)
	if reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Info("-> ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs.Spec)
	log.Infow("ReplicaSet.Status", "status", rs.Status)

	if rs.Meta.NeedsDeletion() {
		log.Info("ReplicaSet.Delete")
		return r.reconcileDeletion(r.delete, rs, &rs.ObjectMeta, log)
	}

	if err = r.ensureFinalizerHeaders(rs, &rs.ObjectMeta, log); err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to update finalizer header: %s", err), log)
	}

	spec := rs.Spec
	podVars := &PodVars{}
	conn, err := r.prepareConnection(request.NamespacedName, spec.CommonSpec, podVars, log)
	if err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to prepare Ops Manager connection: %s", err), log)
	}

	replicaBuilder := r.kubeHelper.NewStatefulSetHelper(rs).
		SetService(rs.ServiceName()).
		SetReplicas(rs.Spec.Members).
		SetPersistence(rs.Spec.Persistent).
		SetPodSpec(NewDefaultPodSpecWrapper(rs.Spec.PodSpec)).
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

	// Watch for changes to primary resource MongoDbReplicaSet
	err = c.Watch(&source.Kind{Type: &mongodb.MongoDbReplicaSet{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mongodb.MongoDbReplicaSet)
			newResource := e.ObjectNew.(*mongodb.MongoDbReplicaSet)
			// We never reconcile on statuses changes - only on spec/metadata ones
			// Note, that in case of failure (when the Reconciler returns (retry, nil)) there is no watch event - so
			// we are safe not to lose retrials. This watch is ONLY for changes done to Mongodb Resource
			if !reflect.DeepEqual(oldResource.GetCommonStatus(), newResource.GetCommonStatus()) {
				return false
			}
			return shouldReconcile(newResource)
		}})
	if err != nil {
		return err
	}

	// Watch for changes to secondary resource Statefulsets and requeue the owner MongoDbStandalone
	err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &mongodb.MongoDbReplicaSet{},
	}, predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// The controller must watch only for changes in spec made by users, we don't care about status changes
			if !reflect.DeepEqual(e.ObjectOld.(*appsv1.StatefulSet).Spec, e.ObjectNew.(*appsv1.StatefulSet).Spec) {
				return true
			}
			return false
		}})
	if err != nil {
		return err
	}

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
func (r *ReconcileMongoDbReplicaSet) updateOmDeploymentRs(omConnection om.Connection, membersNumberBefore int, new *mongodb.MongoDbReplicaSet,
	set *appsv1.StatefulSet, log *zap.SugaredLogger) error {

	err := waitForRsAgentsToRegister(set, new.Spec.ClusterName, omConnection, log)
	if err != nil {
		return err
	}
	replicaSet := buildReplicaSetFromStatefulSet(set, new.Spec.ClusterName, new.Spec.Version)

	err = omConnection.ReadUpdateDeployment(true,
		func(d om.Deployment) error {
			d.MergeReplicaSet(replicaSet, nil)

			d.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)
			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	err = calculateDiffAndStopMonitoringHosts(omConnection, getAllHostsRs(set, new, membersNumberBefore), getAllHostsRs(set, new, new.Spec.Members), log)
	if err != nil {
		return err
	}

	return nil
}

func (r *ReconcileMongoDbReplicaSet) delete(obj interface{}, log *zap.SugaredLogger) error {
	rs := obj.(*mongodb.MongoDbReplicaSet)

	log.Infow("Removing replica set from Ops Manager", "config", rs.Spec)

	conn, err := r.prepareConnection(objectKey(rs.Namespace, rs.Name), rs.Spec.CommonSpec, nil, log)
	if err != nil {
		return err
	}
	err = conn.ReadUpdateDeployment(true,
		func(d om.Deployment) error {
			// error means that replica set is not in the deployment - it's ok and we can proceed (could happen if
			// deletion cleanup happened twice and the first one cleaned OM state already)
			if e := d.RemoveReplicaSetByName(rs.Name); e != nil {
				log.Warnf("Failed to remove replica set from automation config: %s", e)
			}
			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	err = om.StopBackupIfEnabled(conn, rs.Name, om.ReplicaSetType, log)
	if err != nil {
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

func prepareScaleDownReplicaSet(omClient om.Connection, statefulSet *appsv1.StatefulSet, oldMembersCount int, new *mongodb.MongoDbReplicaSet, log *zap.SugaredLogger) error {
	_, podNames := GetDnsForStatefulSetReplicasSpecified(statefulSet, new.Spec.ClusterName, oldMembersCount)
	podNames = podNames[new.Spec.Members:oldMembersCount]

	return prepareScaleDown(omClient, map[string][]string{new.Name: podNames}, log)
}

func getAllHostsRs(set *appsv1.StatefulSet, rs *mongodb.MongoDbReplicaSet, membersCount int) []string {
	if rs == nil {
		return []string{}
	}

	hostnames, _ := GetDnsForStatefulSetReplicasSpecified(set, rs.Spec.ClusterName, membersCount)
	return hostnames
}
