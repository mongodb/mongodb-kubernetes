package operator

import (
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type OpsManagerReconciler struct {
	*ReconcileCommonController
}

var _ reconcile.Reconciler = &OpsManagerReconciler{}

func newOpsManagerReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *OpsManagerReconciler {
	return &OpsManagerReconciler{newReconcileCommonController(mgr, omFunc)}
}

func (r OpsManagerReconciler) createOpsManagerStatefulSet(opsManager *v1.MongoDBOpsManager, log *zap.SugaredLogger) error {

	return nil
}

func (r *OpsManagerReconciler) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("OpsManager", request.NamespacedName)

	opsManager := &v1.MongoDBOpsManager{}

	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatusFailed(opsManager, fmt.Sprintf("Failed to reconcile Ops Manager: %s", err), log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	if reconcileResult, err := r.prepareResourceForReconciliation(request, opsManager, log); reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Info("-> OpsManager.Reconcile")
	log.Infow("OpsManager.Spec", "spec", opsManager.Spec)
	log.Infow("OpsManager.Status", "status", opsManager.Status)

	r.ensureConfiguration(opsManager, log)

	helper := r.kubeHelper.NewOpsManagerStatefulSetHelper(opsManager)
	helper.SetService(opsManager.SvcName()).
		SetLogger(log).
		SetVersion(opsManager.Spec.Version)

	if err := helper.CreateOrUpdateInKubernetes(); err != nil {
		return r.updateStatusFailed(opsManager, fmt.Sprintf("Failed to create/update the StatefulSet: %s", err), log)
	}

	if err := r.waitForOpsManagerToBeReady(log); err != nil {
		log.Warnf("error waiting for Ops Manager to be ready. %s", err)
		return retry()
	}

	return r.updateStatusSuccessful(opsManager, log)
}

func AddOpsManagerController(mgr manager.Manager) error {
	reconciler := newOpsManagerReconciler(mgr, om.NewOpsManagerConnection)
	c, err := controller.New(util.MongoDbOpsManagerController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	// watch for changes to the Ops Manager resources
	eventHandler := MongoDBResourceEventHandler{reconciler: reconciler}

	if err := c.Watch(&source.Kind{Type: &v1.MongoDBOpsManager{}}, &eventHandler, predicatesForOpsManager()); err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbOpsManagerController)
	return nil
}

func (r *OpsManagerReconciler) delete(obj interface{}, log *zap.SugaredLogger) error {
	log.Info("TODO: implement OpsManagerReconciler::delete - manual cleanup required")
	return nil
}

func (r *OpsManagerReconciler) waitForOpsManagerToBeReady(log *zap.SugaredLogger) error {
	// TODO wait for Ops Manager to be ready, work needs to happen in mms codebase here
	log.Info("TODO: implement OpsManagerReconciler::waitForOpsManagerToBeReady - Ops Manager may not be ready")
	return nil
}

// ensureConfiguration makes sure the mandatory configuration is specified
func (r OpsManagerReconciler) ensureConfiguration(opsManager *v1.MongoDBOpsManager, log *zap.SugaredLogger) {
	// update the central URL
	fqdn := GetServiceFQDN(opsManager.SvcName(), opsManager.Namespace, opsManager.ClusterName)

	// todo https
	centralUrl := centralURL(fqdn, util.OpsManagerDefaultPort, false)
	opsManager.AddConfigIfDoesntExist(util.MongoCentralUrlPropKey, centralUrl)

	log.Debugw("Configured property", util.MongoCentralUrlPropKey, centralUrl)

	// todo other properties (managedAppDB, liveMonitoringEnabled)
}

// centralURL constructs the service name that can be used to access Ops Manager from within
// the cluster
func centralURL(fqdn string, port int, https bool) string {
	protocol := "http"
	if https {
		protocol = "https"
	}
	return fmt.Sprintf("%s://%s:%d", protocol, fqdn, port)
}
