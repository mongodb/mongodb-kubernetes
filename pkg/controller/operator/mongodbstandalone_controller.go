package operator

import (
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

// AddStandaloneController creates a new MongoDbStandalone Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddStandaloneController(mgr manager.Manager) error {
	// Create a new controller
	c, err := controller.New(util.MongoDbStandaloneController, mgr, controller.Options{Reconciler: newStandaloneReconciler(mgr, om.NewOpsManagerConnection)})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource MongoDbStandalone
	err = c.Watch(&source.Kind{Type: &mongodb.MongoDbStandalone{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch for changes to secondary resource Statefulsets and requeue the owner MongoDbStandalone
	// TODO pods are owned by Statefulset - we need to check if their changes are reconciled
	err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &mongodb.MongoDbStandalone{},
	})
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbStandaloneController)

	return nil
}

func newStandaloneReconciler(mgr manager.Manager, omFunc om.ConnectionFunc) reconcile.Reconciler {
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

	log.Info(">> Standalone.Reconcile")

	reconcileResult, err := r.fetchResource(request, s, log)
	if reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Debugf("Standalone.Spec[current]: %+v", s.Spec)

	if needsDeletion(s.Meta) {
		log.Info("ReplicaSet.Delete")
		return r.reconcileDeletion(r.delete, s, &s.ObjectMeta, log)
	}

	if err = r.ensureFinalizerHeaders(s, &s.ObjectMeta, log); err != nil {
		return r.updateStatusFailed(s, fmt.Sprintf("Failed to update finalizer header: %s", err), log)
	}

	spec := s.Spec
	podVars := &PodVars{}
	conn, err := r.prepareConnection(s.Namespace, spec.CommonSpec, podVars, log)
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

	r.updateStatusSuccessful(s, log)
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

	conn, err := r.prepareConnection(s.Namespace, s.Spec.CommonSpec, nil, log)
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
