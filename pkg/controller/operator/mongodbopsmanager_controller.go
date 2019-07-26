package operator

import (
	"errors"
	"fmt"
	"math/rand"
	"net"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
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

	if err := r.ensureGenKey(opsManager, log); err != nil {
		return r.updateStatusFailed(opsManager, err.Error(), log)
	}

	r.ensureConfiguration(opsManager, log)

	helper := r.kubeHelper.NewOpsManagerStatefulSetHelper(opsManager)
	helper.SetService(opsManager.SvcName()).
		SetLogger(log).
		SetVersion(opsManager.Spec.Version)

	if err := helper.CreateOrUpdateInKubernetes(); err != nil {
		return r.updateStatusFailed(opsManager, fmt.Sprintf("Failed to create/update the StatefulSet: %s", err), log)
	}

	if err := r.waitForOpsManagerToBeReady(opsManager, log); err != nil {
		return r.updateStatusPending(opsManager, err.Error(), log)
	}

	log.Info("Finished reconciliation for MongoDBOpsManager!")
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

// waitForOpsManagerToBeReady waits until Ops Manager is ready.
// Note, that the waiting time is deliberately made short to make the OpsManager resource more interactive
// to changes/removal. Anyway OpsManager takes crazily long to start so we don't want to block waiting for
// successful start. If unsuccessful - the resource gets into "Pending" state and "info" message is logged.
// The downside is that we can sit in "Pending" forever even if something bad has happened - may be we need to add some
// timer (since last successful start) and log errors if Ops Manager is stuck.
func (r *OpsManagerReconciler) waitForOpsManagerToBeReady(om *v1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	if !util.DoAndRetry(func() (string, bool) {
		// Simple tcp check for port 8080 so far
		host := GetServiceFQDN(om.SvcName(), om.Namespace, om.ClusterName)
		conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", host, util.OpsManagerDefaultPort))
		if err != nil {
			return fmt.Sprintf("Ops Manager (%s:%d) is not accessible", host, util.OpsManagerDefaultPort), false
		}
		defer conn.Close()
		return "", true
	}, log, 6, 5) {
		return errors.New("Ops Manager hasn't started during specified timeout (it may take quite long)")
	}
	return nil
}

// ensureConfiguration makes sure the mandatory configuration is specified
func (r OpsManagerReconciler) ensureConfiguration(opsManager *v1.MongoDBOpsManager, log *zap.SugaredLogger) {
	// update the central URL
	centralUrl := centralURL(opsManager)
	opsManager.AddConfigIfDoesntExist(util.MongoCentralUrlPropKey, centralUrl)

	log.Debugw("Configured property", util.MongoCentralUrlPropKey, centralUrl)

	// todo other properties (managedAppDB, liveMonitoringEnabled)
}

func (r OpsManagerReconciler) ensureGenKey(om *v1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	objectKey := objectKey(om.Namespace, om.Name+"-gen-key")
	_, err := r.kubeHelper.readSecret(objectKey)
	if apiErrors.IsNotFound(err) {
		// todo if the key is not found but the AppDB is initialized - OM will fail to start as preflight
		// check will complain that keys are different - we need to validate against this here

		// the length must be equal to 'EncryptionUtils.DES3_KEY_LENGTH' (24) from mms
		token := make([]byte, 24)
		rand.Read(token)
		keyMap := map[string][]byte{"gen.key": token}

		log.Infof("Creating secret %s", objectKey)
		return r.kubeHelper.createSecret(objectKey, keyMap, map[string]string{})
	}
	return err
}

// centralURL constructs the service name that can be used to access Ops Manager from within
// the cluster
func centralURL(om *v1.MongoDBOpsManager) string {
	fqdn := GetServiceFQDN(om.SvcName(), om.Namespace, om.ClusterName)

	// protocol must be calculated based on tls configuration of the ops manager resource
	protocol := "http"

	return fmt.Sprintf("%s://%s:%d", protocol, fqdn, util.OpsManagerDefaultPort)
}
