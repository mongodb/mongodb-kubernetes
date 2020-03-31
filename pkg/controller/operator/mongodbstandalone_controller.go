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
	err = c.Watch(&source.Kind{Type: &mdbv1.MongoDB{}}, &eventHandler, predicatesFor(mdbv1.Standalone))
	if err != nil {
		return err
	}

	// TODO CLOUDP-35240
	// Watch for changes to secondary resource Statefulsets and requeue the owner MongoDbStandalone
	/*err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
	  	IsController: true,
	  	OwnerType:    &mdbv1.MongoDB{},
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

func (r *ReconcileMongoDbStandalone) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("Standalone", request.NamespacedName)
	s := &mdbv1.MongoDB{}

	mutex := r.GetMutex(request.NamespacedName)
	mutex.Lock()
	defer mutex.Unlock()

	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatusFailed(s, fmt.Sprintf("Failed to reconcile Mongodb Standalone: %s", err), log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	if reconcileResult, err := r.prepareResourceForReconciliation(request, s, log); reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Info("-> Standalone.Reconcile")
	log.Infow("Standalone.Spec", "spec", s.Spec)
	log.Infow("Standalone.Status", "status", s.Status)

	projectConfig, err := r.kubeHelper.readProjectConfig(request.Namespace, s.Spec.GetProject())
	if err != nil {
		log.Infof("error reading project %s", err)
		return retry()
	}

	s.Spec.SetParametersFromConfigMap(projectConfig)

	podVars := &PodVars{}
	conn, err := r.prepareConnection(request.NamespacedName, s.Spec.ConnectionSpec, podVars, log)
	if err != nil {
		return r.updateStatusFailed(s, fmt.Sprintf("Failed to prepare Ops Manager connection: %s", err), log)
	}

	reconcileResult := checkIfHasExcessProcesses(conn, s, log)
	if !reconcileResult.isOk() {
		return reconcileResult.updateStatus(s, r.ReconcileCommonController, log)
	}

	// cannot have a non-tls deployment in an x509 environment
	authSpec := s.Spec.Security.Authentication
	if authSpec.Enabled && authSpec.IsX509Enabled() && !s.Spec.GetTLSConfig().Enabled {
		return r.updateStatusValidationFailure(s, fmt.Sprintf("cannot have a non-tls deployment when x509 authentication is enabled"), log, true)
	}

	standaloneBuilder := r.kubeHelper.NewStatefulSetHelper(s).
		SetReplicas(1).
		SetService(s.ServiceName()).
		SetPodVars(podVars).
		SetLogger(log).
		SetTLS(s.Spec.GetTLSConfig()).
		SetProjectConfig(*projectConfig).
		SetSecurity(s.Spec.Security)
	standaloneBuilder.SetCertificateHash(standaloneBuilder.readPemHashFromSecret())

	if status := validateMongoDBResource(s, conn); !status.isOk() {
		return status.updateStatus(s, r.ReconcileCommonController, log)
	}

	if status := r.kubeHelper.ensureSSLCertsForStatefulSet(standaloneBuilder, log); !status.isOk() {
		return status.updateStatus(s, r.ReconcileCommonController, log)
	}

	if status := r.ensureX509InKubernetes(s, standaloneBuilder, log); !status.isOk() {
		return status.updateStatus(s, r.ReconcileCommonController, log)
	}

	status := runInGivenOrder(standaloneBuilder.needToPublishStateFirst(log),
		func() reconcileStatus {
			sts, err := standaloneBuilder.BuildStatefulSet()
			if err != nil {
				return failed("Failed to create/update (Ops Manager reconciliation phase): %s", err.Error())
			}
			return updateOmDeployment(conn, s, sts, log).onErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() reconcileStatus {
			if err := standaloneBuilder.CreateOrUpdateInKubernetes(); err != nil {
				return failed("Failed to create/update (Kubernetes reconciliation phase): %s", err.Error())
			}

			if !r.kubeHelper.isStatefulSetUpdated(standaloneBuilder.Namespace, standaloneBuilder.Name, log) {
				return pending(fmt.Sprintf("MongoDB %s resource is still starting", standaloneBuilder.Name))
			}

			log.Info("Updated statefulset for standalone")
			return ok()
		})

	if !status.isOk() {
		return status.updateStatus(s, r.ReconcileCommonController, log)
	}

	log.Infof("Finished reconciliation for MongoDbStandalone! %s", completionMessage(conn.BaseURL(), conn.GroupID()))

	return reconcileResult.updateStatus(s, r.ReconcileCommonController, log, DeploymentLink(conn.BaseURL(), conn.GroupID()))
}

func updateOmDeployment(conn om.Connection, s *mdbv1.MongoDB,
	set appsv1.StatefulSet, log *zap.SugaredLogger) reconcileStatus {
	if err := waitForRsAgentsToRegister(set, s.Spec.GetClusterDomain(), conn, log); err != nil {
		return failedErr(err)
	}

	status, additionalReconciliationRequired := updateOmAuthentication(conn, []string{set.Name}, s, log)
	if !status.isOk() {
		return status
	}

	standaloneOmObject := createProcess(set, s)
	err := conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			excessProcesses := d.GetNumberOfExcessProcesses(s.Name)
			if excessProcesses > 0 {
				return fmt.Errorf("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)")
			}
			d.MergeStandalone(standaloneOmObject, nil)
			d.AddMonitoringAndBackup(standaloneOmObject.HostName(), log)
			d.ConfigureTLS(s.Spec.GetTLSConfig())
			return nil
		},
		log,
	)

	if err != nil {
		return failedErr(err)
	}

	if err := om.WaitForReadyState(conn, []string{set.Name}, log); err != nil {
		return failedErr(err)
	}

	if additionalReconciliationRequired {
		return pending("Performing multi stage reconciliation")
	}

	log.Info("Updated Ops Manager for standalone")
	return ok()

}

func (r *ReconcileMongoDbStandalone) delete(obj interface{}, log *zap.SugaredLogger) error {
	s := obj.(*mdbv1.MongoDB)

	log.Infow("Removing standalone from Ops Manager", "config", s.Spec)

	conn, err := r.prepareConnection(objectKey(s.Namespace, s.Name), s.Spec.ConnectionSpec, nil, log)
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
		log,
	)
	if err != nil {
		return fmt.Errorf("Failed to update Ops Manager automation config: %s", err)
	}

	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return err
	}

	hostsToRemove, _ := util.GetDNSNames(s.Name, s.ServiceName(), s.Namespace, s.Spec.GetClusterDomain(), 1)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err = om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}
	log.Info("Removed standalone from Ops Manager!")
	return nil
}

func createProcess(set appsv1.StatefulSet, s *mdbv1.MongoDB) om.Process {
	hostnames, _ := util.GetDnsForStatefulSet(set, s.Spec.GetClusterDomain())
	wiredTigerCache := calculateWiredTigerCache(set, s.Spec.GetVersion())

	process := om.NewMongodProcess(s.Name, hostnames[0], s)
	if wiredTigerCache != nil {
		process.SetWiredTigerCache(*wiredTigerCache)
	}
	return process
}
