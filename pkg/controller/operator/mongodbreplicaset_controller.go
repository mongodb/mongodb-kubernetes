package operator

import (
	"errors"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"

	"go.uber.org/zap"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	appsv1 "k8s.io/api/apps/v1"

	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// AddReplicaSetController creates a new MongoDbReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddReplicaSetController(mgr manager.Manager) error {
	// Create a new controller
	c, err := controller.New(util.MongoDbReplicaSetController, mgr, controller.Options{Reconciler: newReplicaSetReconciler(mgr, om.NewOpsManagerConnection)})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource MongoDbReplicaSet
	err = c.Watch(&source.Kind{Type: &mongodb.MongoDbReplicaSet{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch for changes to secondary resource Statefulsets and requeue the owner MongoDbStandalone
	// TODO pods are owned by Statefulset - we need to check if their changes are reconciled as well
	err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &mongodb.MongoDbReplicaSet{},
	})
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbReplicaSetController)

	return nil
}

func newReplicaSetReconciler(mgr manager.Manager, omFunc func(baseUrl, groupId, user, publicApiKey string) om.OmConnection) reconcile.Reconciler {
	return &ReconcileMongoDbReplicaSet{newReconcileCommonController(mgr, omFunc)}
}

var _ reconcile.Reconciler = &ReconcileMongoDbStandalone{}

// ReconcileMongoDbReplicaSet reconciles a MongoDbReplicaSet object
type ReconcileMongoDbReplicaSet struct {
	*ReconcileCommonController
}

// Reconcile reads that state of the cluster for a MongoDbReplicaSet object and makes changes based on the state read
// and what is in the MongoDbReplicaSet.Spec
func (r *ReconcileMongoDbReplicaSet) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("replica set", request.NamespacedName)

	rs := &mongodb.MongoDbReplicaSet{}

	defer exceptionHandling(
		func() (reconcile.Result, error) {
			return r.updateStatusFailed(rs, "Failed to reconcile Mongodb Replica Set", log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	log.Info(">> Reconciling MongoDbReplicaSet")

	// Fetch the MongoDbReplicaSet instance

	ok, err := r.fetchResource(request, rs, log)
	if !ok {
		return reconcile.Result{}, err
	}

	log.Debugf("Spec for MongoDbReplicaSet: %+v\n", rs.Spec)

	// 'ObjectMeta.DeletionTimestamp' field is non zero if the object is being deleted
	if rs.ObjectMeta.DeletionTimestamp.IsZero() {
		if err = r.ensureFinalizerHeaders(rs, &rs.ObjectMeta, log); err != nil {
			return r.updateStatusFailed(rs, fmt.Sprintf("Failed to update finalizer header: %s", err), log)
		}
	} else {
		return r.reconcileDeletion(r.onDeleteReplicaSet, rs, &rs.ObjectMeta, log)
	}

	// TODO seems the validation for changes must be performed using controller runtime events interception
	/*if err := validateReplicaSetUpdate(o, n); err != nil {
		log.Error(err)
		return
	}*/

	spec := rs.Spec
	conn, podVars, err := r.prepareOmConnection(rs.Namespace, spec.CommonSpec, log)
	if err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to prepare Ops Manager connection: %s", err), log)
	}

	currentMembersCount := rs.Status.Members

	scaleDown := spec.Members < currentMembersCount

	replicaBuilder := r.kubeHelper.NewStatefulSetHelper(rs).
		SetService(rs.ServiceName()).
		SetReplicas(rs.Spec.Members).
		SetPersistence(rs.Spec.Persistent).
		SetPodSpec(NewDefaultPodSpecWrapper(rs.Spec.PodSpec)).
		SetPodVars(podVars).
		SetExposedExternally(true).
		SetLogger(log)
	replicaSetObject := replicaBuilder.BuildStatefulSet()

	if scaleDown {
		if err := prepareScaleDownReplicaSet(conn, replicaSetObject, currentMembersCount, rs, log); err != nil {
			return r.updateStatusFailed(rs, fmt.Sprintf("Failed to prepare Replica Set for scaling down using Ops Manager: %s", err), log)
		}
	}

	err = replicaBuilder.CreateOrUpdateInKubernetes()
	if err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to create/update the StatefulSet: %s", err), log)
	}

	log.Info("Updated statefulset for replica set")

	if err := r.updateOmDeploymentRs(conn, currentMembersCount, rs, replicaSetObject, log); err != nil {
		return r.updateStatusFailed(rs, fmt.Sprintf("Failed to create/update replica set in Ops Manager: %s", err), log)
	}

	r.updateStatusSuccessful(rs, log)
	log.Infof("Finished reconciliation for MongoDbReplicaSet! %s", completionMessage(conn.BaseUrl(), conn.GroupId()))
	return reconcile.Result{}, nil
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (c *ReconcileMongoDbReplicaSet) updateOmDeploymentRs(omConnection om.OmConnection, membersNumberBefore int, new *mongodb.MongoDbReplicaSet,
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

	if err := deleteHostnamesFromMonitoring(omConnection, getAllHostsRs(set, new, membersNumberBefore), getAllHostsRs(set, new, new.Spec.Members), log); err != nil {
		return err
	}

	return nil
}

func (c *ReconcileMongoDbReplicaSet) onDeleteReplicaSet(obj interface{}, log *zap.SugaredLogger) error {
	rs := obj.(*mongodb.MongoDbReplicaSet)

	log.Infow("Removing replica set from Ops Manager", "config", rs.Spec)

	conn, _, err := c.prepareOmConnection(rs.Namespace, rs.Spec.CommonSpec, log)
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
		return fmt.Errorf("Failed to update Ops Manager automation config: %s.", err)
	}

	err = om.StopBackupIfEnabled(conn, rs.Name, om.ReplicaSetType, log)
	if err != nil {
		return fmt.Errorf("Failed to disable backup for replica set: %s", err)
	}

	hostsToRemove, _ := GetDnsNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.ClusterName, rs.Spec.Members)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err = om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return fmt.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
	}

	log.Info("Removed replica set from Ops Manager!")
	return nil
}

func validateReplicaSetUpdate(oldSpec, newSpec *mongodb.MongoDbReplicaSet) error {
	if newSpec.Namespace != oldSpec.Namespace {
		return errors.New("Namespace cannot change for existing object")
	}

	if newSpec.Spec.ClusterName != oldSpec.Spec.ClusterName {
		return errors.New("Cluster name cannot change for existing object")
	}

	return nil
}

func prepareScaleDownReplicaSet(omClient om.OmConnection, statefulSet *appsv1.StatefulSet, oldMembersCount int, new *mongodb.MongoDbReplicaSet,
	log *zap.SugaredLogger) error {
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
