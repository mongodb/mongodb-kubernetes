package operator

import (
	"context"
	"fmt"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"

	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault/vaultwatcher"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
	eventHandler := ResourceEventHandler{deleter: reconciler}
	err = c.Watch(&source.Kind{Type: &mdbv1.MongoDB{}}, &eventHandler, watch.PredicatesForMongoDB(mdbv1.Standalone))
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
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.Secret, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	// if vault secret backend is enabled watch for Vault secret change and trigger reconcile
	if vault.IsVaultSecretBackend() {
		eventChannel := make(chan event.GenericEvent)
		go vaultwatcher.WatchSecretChangeForMDB(zap.S(), eventChannel, reconciler.client, reconciler.VaultClient, mdbv1.Standalone)

		err = c.Watch(
			&source.Channel{Source: eventChannel},
			&handler.EnqueueRequestForObject{},
		)
		if err != nil {
			zap.S().Errorf("Failed to watch for vault secret changes: %w", err)
		}
	}
	zap.S().Infof("Registered controller %s", util.MongoDbStandaloneController)

	return nil
}

func newStandaloneReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *ReconcileMongoDbStandalone {
	return &ReconcileMongoDbStandalone{
		ReconcileCommonController: newReconcileCommonController(mgr),
		ResourceWatcher:           watch.NewResourceWatcher(),
		omConnectionFactory:       omFunc,
	}
}

// ReconcileMongoDbStandalone reconciles a MongoDbStandalone object
type ReconcileMongoDbStandalone struct {
	*ReconcileCommonController
	watch.ResourceWatcher
	omConnectionFactory om.ConnectionFactory
}

func (r *ReconcileMongoDbStandalone) Reconcile(_ context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	agents.UpgradeAllIfNeeded(r.client, r.SecretClient, r.omConnectionFactory, GetWatchedNamespace())

	log := zap.S().With("Standalone", request.NamespacedName)
	s := &mdbv1.MongoDB{}

	if reconcileResult, err := r.prepareResourceForReconciliation(request, s, log); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcileResult, err
	}

	if err := s.ProcessValidationsOnReconcile(nil); err != nil {
		return r.updateStatus(s, workflow.Invalid(err.Error()), log)
	}

	log.Info("-> Standalone.Reconcile")
	log.Infow("Standalone.Spec", "spec", s.Spec)
	log.Infow("Standalone.Status", "status", s.Status)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, s, log)
	if err != nil {
		return r.updateStatus(s, workflow.Failed(err.Error()), log)
	}

	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, s.Namespace, log)
	if err != nil {
		return r.updateStatus(s, workflow.Failed("Failed to prepare Ops Manager connection: %s", err), log)
	}

	if status := ensureSupportedOpsManagerVersion(conn); status.Phase() != mdbstatus.PhaseRunning {
		return r.updateStatus(s, status, log)
	}

	r.RegisterWatchedMongodbResources(s.ObjectKey(), s.Spec.GetProject(), s.Spec.Credentials)

	reconcileResult := checkIfHasExcessProcesses(conn, s, log)
	if !reconcileResult.IsOK() {
		return r.updateStatus(s, reconcileResult, log)
	}

	if status := controlledfeature.EnsureFeatureControls(*s, conn, conn.OpsManagerVersion(), log); !status.IsOK() {
		return r.updateStatus(s, status, log)
	}

	// cannot have a non-tls deployment in an x509 environment
	// TODO move to webhook validations
	security := s.Spec.Security
	if security.Authentication != nil && security.Authentication.Enabled && security.Authentication.IsX509Enabled() && !s.Spec.GetSecurity().IsTLSEnabled() {
		return r.updateStatus(s, workflow.Invalid("cannot have a non-tls deployment when x509 authentication is enabled"), log)
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return r.updateStatus(s, workflow.Failed(err.Error()), log)
	}

	podVars := newPodVars(conn, projectConfig, s.Spec.ConnectionSpec)

	if status := validateMongoDBResource(s, conn); !status.IsOK() {
		return r.updateStatus(s, status, log)
	}

	if status := certs.EnsureSSLCertsForStatefulSet(r.SecretClient, r.SecretClient, *s.Spec.Security, certs.StandaloneConfig(*s), log); !status.IsOK() {
		return r.updateStatus(s, status, log)
	}

	// TODO separate PR
	certConfigurator := certs.StandaloneX509CertConfigurator{MongoDB: s, SecretClient: r.SecretClient}
	if status := r.ensureX509SecretAndCheckTLSType(certConfigurator, currentAgentAuthMode, log); !status.IsOK() {
		return r.updateStatus(s, status, log)
	}

	if status := ensureRoles(s.Spec.GetSecurity().Roles, conn, log); !status.IsOK() {
		return r.updateStatus(s, status, log)
	}

	var vaultConfig vault.VaultConfiguration
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
	}
	standaloneCertSecretName := certs.StandaloneConfig(*s).CertSecretName

	var databaseSecretPath string
	if r.VaultClient != nil {
		databaseSecretPath = r.VaultClient.DatabaseSecretPath()
	}
	standaloneOpts := construct.StandaloneOptions(
		CertificateHash(pem.ReadHashFromSecret(r.SecretClient, s.Namespace, standaloneCertSecretName, databaseSecretPath, log)),
		CurrentAgentAuthMechanism(currentAgentAuthMode),
		PodEnvVars(podVars),
		WithVaultConfig(vaultConfig),
	)

	sts := construct.DatabaseStatefulSet(*s, standaloneOpts)

	status := workflow.RunInGivenOrder(needToPublishStateFirst(r.client, *s, standaloneOpts, log),
		func() workflow.Status {
			return r.updateOmDeployment(conn, s, sts, log).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			if err = create.DatabaseInKubernetes(r.client, *s, sts, standaloneOpts, log); err != nil {
				return workflow.Failed("Failed to create/update (Kubernetes reconciliation phase): %s", err.Error())
			}

			if status := getStatefulSetStatus(sts.Namespace, sts.Name, r.client); !status.IsOK() {
				return status
			}
			_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)

			log.Info("Updated StatefulSet for standalone")
			return workflow.OK()
		})

	if !status.IsOK() {
		return r.updateStatus(s, status, log)
	}

	annotationsToAdd, err := getAnnotationsForResource(s)
	if err != nil {
		return r.updateStatus(s, workflow.Failed(err.Error()), log)
	}

	if vault.IsVaultSecretBackend() {
		secrets := s.GetSecretsMountedIntoDBPod()
		vaultMap := make(map[string]string)
		for _, secret := range secrets {
			path := fmt.Sprintf("%s/%s/%s", r.VaultClient.DatabaseSecretMetadataPath(), s.Namespace, secret)
			vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		}
		path := fmt.Sprintf("%s/%s/%s", r.VaultClient.OperatorScretMetadataPath(), s.Namespace, s.Spec.Credentials)
		vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		for k, val := range vaultMap {
			annotationsToAdd[k] = val
		}
	}
	if err := annotations.SetAnnotations(s.DeepCopy(), annotationsToAdd, r.client); err != nil {
		return r.updateStatus(s, workflow.Failed(err.Error()), log)
	}

	log.Infof("Finished reconciliation for MongoDbStandalone! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return r.updateStatus(s, status, log, mdbstatus.NewBaseUrlOption(deployment.Link(conn.BaseURL(), conn.GroupID())))
}

func (r *ReconcileMongoDbStandalone) updateOmDeployment(conn om.Connection, s *mdbv1.MongoDB,
	set appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {
	if err := agents.WaitForRsAgentsToRegister(set, 0, s.Spec.GetClusterDomain(), conn, log, s); err != nil {
		return workflow.Failed(err.Error())
	}

	// TODO standalone PR
	status, additionalReconciliationRequired := r.updateOmAuthentication(conn, []string{set.Name}, s, "", "", log)
	if !status.IsOK() {
		return status
	}

	standaloneOmObject := createProcess(set, util.DatabaseContainerName, s)
	err := conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			excessProcesses := d.GetNumberOfExcessProcesses(s.Name)
			if excessProcesses > 0 {
				return fmt.Errorf("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)")
			}

			lastStandaloneConfig, err := s.GetLastAdditionalMongodConfigByType(mdbv1.StandaloneConfig)
			if err != nil {
				return err
			}

			d.MergeStandalone(standaloneOmObject, s.Spec.AdditionalMongodConfig.ToMap(), lastStandaloneConfig.ToMap(), nil)
			// TODO change last argument in separate PR
			d.AddMonitoringAndBackup(log, s.Spec.GetSecurity().IsTLSEnabled(), util.CAFilePathInContainer)
			d.ConfigureTLS(s.Spec.GetSecurity(), util.CAFilePathInContainer)
			return nil
		},
		log,
	)

	if err != nil {
		return workflow.Failed(err.Error())
	}

	if err := om.WaitForReadyState(conn, []string{set.Name}, log); err != nil {
		return workflow.Failed(err.Error())
	}

	if additionalReconciliationRequired {
		return workflow.Pending("Performing multi stage reconciliation")
	}

	log.Info("Updated Ops Manager for standalone")
	return workflow.OK()

}

func (r *ReconcileMongoDbStandalone) OnDelete(obj runtime.Object, log *zap.SugaredLogger) error {
	s := obj.(*mdbv1.MongoDB)

	log.Infow("Removing standalone from Ops Manager", "config", s.Spec)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, s, log)
	if err != nil {
		return err
	}

	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, s.Namespace, log)
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
		return fmt.Errorf("failed to update Ops Manager automation config: %s", err)
	}

	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return err
	}

	hostsToRemove, _ := dns.GetDNSNames(s.Name, s.ServiceName(), s.Namespace, s.Spec.GetClusterDomain(), 1, nil)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err = host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}
	if err := r.clearProjectAuthenticationSettings(conn, s, processNames, log); err != nil {
		return err
	}

	r.RemoveDependentWatchedResources(s.ObjectKey())

	log.Info("Removed standalone from Ops Manager!")
	return nil
}

func createProcess(set appsv1.StatefulSet, containerName string, s *mdbv1.MongoDB) om.Process {
	hostnames, _ := dns.GetDnsForStatefulSet(set, s.Spec.GetClusterDomain(), nil)
	process := om.NewMongodProcess(0, s.Name, hostnames[0], s.Spec.GetAdditionalMongodConfig(), s.GetSpec(), "")
	return process
}
