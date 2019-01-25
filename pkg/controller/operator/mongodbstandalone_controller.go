package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"

	"go.uber.org/zap"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	appsv1 "k8s.io/api/apps/v1"
	"reflect"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// AddStandaloneController creates a new MongoDbStandalone Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddStandaloneController(mgr manager.Manager) error {
	// Create a new controller
	reconciler := newStandaloneReconciler(mgr, om.NewOpsManagerConnection)
	c, err := controller.New(util.MongoDbStandaloneController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource MongoDbStandalone
	err = c.Watch(&source.Kind{Type: &mongodb.MongoDbStandalone{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mongodb.MongoDbStandalone)
			newResource := e.ObjectNew.(*mongodb.MongoDbStandalone)
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
	// TODO pods are owned by Statefulset - we need to check if their changes are reconciled
	err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &mongodb.MongoDbStandalone{},
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

	zap.S().Infof("Registered controller %s", util.MongoDbStandaloneController)

	return nil
}

func newStandaloneReconciler(mgr manager.Manager, omFunc om.ConnectionFunc) *ReconcileMongoDbStandalone {
	return &ReconcileMongoDbStandalone{newReconcileCommonController(mgr, omFunc)}
}

var _ reconcile.Reconciler = &ReconcileMongoDbStandalone{}

// ReconcileMongoDbStandalone reconciles a MongoDbStandalone object
type ReconcileMongoDbStandalone struct {
	*ReconcileCommonController
}

// Reconcile reads that state of the cluster for a MongoDbStandalone object and makes changes based on the state read
// and what is in the MongoDbStandalone.Spec
func (r *ReconcileMongoDbStandalone) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("standalone", request.NamespacedName)

	s := &mongodb.MongoDbStandalone{}
	defer exceptionHandling(
		func() (reconcile.Result, error) {
			return r.updateStatusFailed(s, "Failed to reconcile Mongodb Standalone", log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	reconcileResult, err := r.prepareResourceForReconciliation(request, s, log)
	if reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Info("-> Standalone.Reconcile")
	log.Debugf("Standalone.Spec[current]: %+v", s.Spec)

	if s.Meta.NeedsDeletion() {
		log.Info("ReplicaSet.Delete")
		return r.reconcileDeletion(r.delete, s, &s.ObjectMeta, log)
	}

	if err = r.ensureFinalizerHeaders(s, &s.ObjectMeta, log); err != nil {
		return r.updateStatusFailed(s, fmt.Sprintf("Failed to update finalizer header: %s", err), log)
	}

	spec := s.Spec
	podVars := &PodVars{}
	conn, err := r.prepareConnection(request.NamespacedName, spec.CommonSpec, podVars, log)
	if err != nil {
		return r.updateStatusFailed(s, fmt.Sprintf("Failed to prepare Ops Manager connection: %s", err), log)
	}

	standaloneBuilder := r.kubeHelper.NewStatefulSetHelper(s).
		SetService(s.ServiceName()).
		SetPersistence(s.Spec.Persistent).
		SetPodSpec(NewDefaultStandalonePodSpecWrapper(s.Spec.PodSpec)).
		SetPodVars(podVars).
		SetExposedExternally(true).
		SetLogger(log)

	err = standaloneBuilder.CreateOrUpdateInKubernetes()
	if err != nil {
		return r.updateStatusFailed(s, fmt.Sprintf("Failed to create/update the StatefulSet: %s", err), log)
	}

	log.Info("Updated statefulset for standalone")

	if err := updateOmDeployment(conn, s, standaloneBuilder.BuildStatefulSet(), log); err != nil {
		return r.updateStatusFailed(s, fmt.Sprintf("Failed to create/update standalone in Ops Manager: %s", err), log)
	}

	r.updateStatusSuccessful(s, log, conn.BaseURL(), conn.GroupID())
	log.Infof("Finished reconciliation for MongoDbStandalone! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return reconcile.Result{}, nil
}

func updateOmDeployment(omConnection om.Connection, s *mongodb.MongoDbStandalone,
	set *appsv1.StatefulSet, log *zap.SugaredLogger) error {
	if err := waitForRsAgentsToRegister(set, s.Spec.ClusterName, omConnection, log); err != nil {
		return err
	}

	standaloneOmObject := createProcess(set, s)
	err := omConnection.ReadUpdateDeployment(true,
		func(d om.Deployment) error {
			d.MergeStandalone(standaloneOmObject, nil)
			d.AddMonitoringAndBackup(standaloneOmObject.HostName(), log)

			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	return nil
}

func (r *ReconcileMongoDbStandalone) delete(obj interface{}, log *zap.SugaredLogger) error {
	s := obj.(*mongodb.MongoDbStandalone)

	log.Infow("Removing standalone from Ops Manager", "config", s.Spec)

	conn, err := r.prepareConnection(objectKey(s.Namespace, s.Name), s.Spec.CommonSpec, nil, log)
	if err != nil {
		return err
	}

	err = conn.ReadUpdateDeployment(true,
		func(d om.Deployment) error {
			// error means that process is not in the deployment - it's ok and we can proceed (could happen if
			// deletion cleanup happened twice and the first one cleaned OM state already)
			d.RemoveProcessByName(s.Name)
			return nil
		},
		log,
	)
	if err != nil {
		return fmt.Errorf("Failed to update Ops Manager automation config: %s", err)
	}

	hostsToRemove, _ := GetDnsNames(s.Name, s.ServiceName(), s.Namespace, s.Spec.ClusterName, 1)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err := om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return fmt.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
	}

	log.Info("Removed standalone from Ops Manager!")
	return nil
}

func createProcess(set *appsv1.StatefulSet, s *mongodb.MongoDbStandalone) om.Process {
	hostnames, _ := GetDnsForStatefulSet(set, s.Spec.ClusterName)
	wiredTigerCache := calculateWiredTigerCache(set)

	process := om.NewMongodProcess(s.Name, hostnames[0], s.Spec.Version)
	if wiredTigerCache != nil {
		process.SetWiredTigerCache(*wiredTigerCache)
	}
	return process
}
