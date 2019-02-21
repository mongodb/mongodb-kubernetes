package operator

import (
	"fmt"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
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

	// watch for changes to standalone MongoDB resources
	eventHandler := MongoDBResourceEventHandler{reconciler: reconciler}
	err = c.Watch(&source.Kind{Type: &mongodb.MongoDbStandalone{}}, &eventHandler, predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mongodb.MongoDbStandalone)
			newResource := e.ObjectNew.(*mongodb.MongoDbStandalone)
			return shouldReconcile(oldResource, newResource)
		}})
	if err != nil {
		return err
	}

	// TODO CLOUDP-35240
	// Watch for changes to secondary resource Statefulsets and requeue the owner MongoDbStandalone
	/*err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
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
	}*/

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

func newStandaloneReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *ReconcileMongoDbStandalone {
	return &ReconcileMongoDbStandalone{newReconcileCommonController(mgr, omFunc)}
}

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
	log.Infow("Standalone.Spec", "spec", s.Spec)
	log.Infow("Standalone.Status", "status", s.Status)

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

func updateOmDeployment(conn om.Connection, s *mongodb.MongoDbStandalone,
	set *appsv1.StatefulSet, log *zap.SugaredLogger) error {
	if err := waitForRsAgentsToRegister(set, s.Spec.ClusterName, conn, log); err != nil {
		return err
	}

	processNames := make([]string, 0)
	standaloneOmObject := createProcess(set, s)
	err := conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			d.MergeStandalone(standaloneOmObject, nil)
			d.AddMonitoringAndBackup(standaloneOmObject.HostName(), log)
			processNames = d.GetProcessNames(om.Standalone{}, s.Name)
			return nil
		},
		getMutex(conn.GroupName(), conn.OrgID()),
		log,
	)

	if err != nil {
		return err
	}

	return om.WaitForReadyState(conn, processNames, log)

}

func (r *ReconcileMongoDbStandalone) delete(obj interface{}, log *zap.SugaredLogger) error {
	s := obj.(*mongodb.MongoDbStandalone)

	log.Infow("Removing standalone from Ops Manager", "config", s.Spec)

	conn, err := r.prepareConnection(objectKey(s.Namespace, s.Name), s.Spec.CommonSpec, nil, log)
	if err != nil {
		return err
	}

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			processNames = d.GetProcessNames(om.Standalone{}, s.Name)
			// error means that process is not in the deployment - it's ok and we can proceed (could happen if
			// deletion cleanup happened twice and the first one cleaned OM state already)
			if e := d.RemoveProcessByName(s.Name, log); e != nil {
				log.Warnf("Failed to remove standalone from automation config: %s", e)
			}
			return nil
		},
		getMutex(conn.GroupName(), conn.OrgID()),
		log,
	)
	if err != nil {
		return fmt.Errorf("Failed to update Ops Manager automation config: %s", err)
	}

	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return err
	}

	hostsToRemove, _ := GetDnsNames(s.Name, s.ServiceName(), s.Namespace, s.Spec.ClusterName, 1)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err = om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
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
