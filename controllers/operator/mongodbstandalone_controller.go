package operator

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"

	mcoConstruct "github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/images"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault/vaultwatcher"
)

// AddStandaloneController creates a new MongoDbStandalone Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddStandaloneController(ctx context.Context, mgr manager.Manager, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise bool) error {
	// Create a new controller
	reconciler := newStandaloneReconciler(ctx, mgr.GetClient(), imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, om.NewOpsManagerConnection)
	c, err := controller.New(util.MongoDbStandaloneController, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)})
	if err != nil {
		return err
	}

	// watch for changes to standalone MongoDB resources
	eventHandler := ResourceEventHandler{deleter: reconciler}
	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbv1.MongoDB{}, &eventHandler, watch.PredicatesForMongoDB(mdbv1.Standalone)))
	if err != nil {
		return err
	}

	err = c.Watch(
		source.Channel[client.Object](OmUpdateChannel,
			&handler.EnqueueRequestForObject{},
			source.WithPredicates(watch.PredicatesForMongoDB(mdbv1.Standalone)),
		))
	if err != nil {
		return xerrors.Errorf("not able to setup OmUpdateChannel to listent to update events from OM: %s", err)
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.ConfigMap{},
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.Secret{},
		&watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	// if vault secret backend is enabled watch for Vault secret change and trigger reconcile
	if vault.IsVaultSecretBackend() {
		eventChannel := make(chan event.GenericEvent)
		go vaultwatcher.WatchSecretChangeForMDB(ctx, zap.S(), eventChannel, reconciler.client, reconciler.VaultClient, mdbv1.Standalone)

		err = c.Watch(
			source.Channel[client.Object](eventChannel,
				&handler.EnqueueRequestForObject{},
			))
		if err != nil {
			zap.S().Errorf("Failed to watch for vault secret changes: %w", err)
		}
	}
	zap.S().Infof("Registered controller %s", util.MongoDbStandaloneController)

	return nil
}

func newStandaloneReconciler(ctx context.Context, kubeClient client.Client, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise bool, omFunc om.ConnectionFactory) *ReconcileMongoDbStandalone {
	return &ReconcileMongoDbStandalone{
		ReconcileCommonController: NewReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:       omFunc,
		imageUrls:                 imageUrls,
		forceEnterprise:           forceEnterprise,

		initDatabaseNonStaticImageVersion: initDatabaseNonStaticImageVersion,
		databaseNonStaticImageVersion:     databaseNonStaticImageVersion,
	}
}

// ReconcileMongoDbStandalone reconciles a MongoDbStandalone object
type ReconcileMongoDbStandalone struct {
	*ReconcileCommonController
	omConnectionFactory om.ConnectionFactory
	imageUrls           images.ImageUrls
	forceEnterprise     bool

	initDatabaseNonStaticImageVersion string
	databaseNonStaticImageVersion     string
}

func (r *ReconcileMongoDbStandalone) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("Standalone", request.NamespacedName)
	s := &mdbv1.MongoDB{}

	if reconcileResult, err := r.prepareResourceForReconciliation(ctx, request, s, log); err != nil {
		if errors.IsNotFound(err) {
			return workflow.Invalid("Object for reconciliation not found").ReconcileResult()
		}
		return reconcileResult, err
	}

	if !architectures.IsRunningStaticArchitecture(s.Annotations) {
		agents.UpgradeAllIfNeeded(ctx, agents.ClientSecret{Client: r.client, SecretClient: r.SecretClient}, r.omConnectionFactory, GetWatchedNamespace(), false)
	}

	if err := s.ProcessValidationsOnReconcile(nil); err != nil {
		return r.updateStatus(ctx, s, workflow.Invalid("%s", err.Error()), log)
	}

	log.Info("-> Standalone.Reconcile")
	log.Infow("Standalone.Spec", "spec", s.Spec)
	log.Infow("Standalone.Status", "status", s.Status)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.client, r.SecretClient, s, log)
	if err != nil {
		return r.updateStatus(ctx, s, workflow.Failed(err), log)
	}

	conn, _, err := connection.PrepareOpsManagerConnection(ctx, r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, s.Namespace, log)
	if err != nil {
		return r.updateStatus(ctx, s, workflow.Failed(xerrors.Errorf("Failed to prepare Ops Manager connection: %w", err)), log)
	}

	if status := ensureSupportedOpsManagerVersion(conn); status.Phase() != mdbstatus.PhaseRunning {
		return r.updateStatus(ctx, s, status, log)
	}

	r.SetupCommonWatchers(s, nil, nil, s.Name)

	reconcileResult := checkIfHasExcessProcesses(conn, s.Name, log)
	if !reconcileResult.IsOK() {
		return r.updateStatus(ctx, s, reconcileResult, log)
	}

	if status := controlledfeature.EnsureFeatureControls(*s, conn, conn.OpsManagerVersion(), log); !status.IsOK() {
		return r.updateStatus(ctx, s, status, log)
	}

	// cannot have a non-tls deployment in an x509 environment
	// TODO move to webhook validations
	security := s.Spec.Security
	if security.Authentication != nil && security.Authentication.Enabled && security.Authentication.IsX509Enabled() && !s.Spec.GetSecurity().IsTLSEnabled() {
		return r.updateStatus(ctx, s, workflow.Invalid("cannot have a non-tls deployment when x509 authentication is enabled"), log)
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return r.updateStatus(ctx, s, workflow.Failed(err), log)
	}

	podVars := newPodVars(conn, projectConfig, s.Spec.LogLevel)

	if status := validateMongoDBResource(s, conn); !status.IsOK() {
		return r.updateStatus(ctx, s, status, log)
	}

	if status := certs.EnsureSSLCertsForStatefulSet(ctx, r.SecretClient, r.SecretClient, *s.Spec.Security, certs.StandaloneConfig(*s), log); !status.IsOK() {
		return r.updateStatus(ctx, s, status, log)
	}

	// TODO separate PR
	certConfigurator := certs.StandaloneX509CertConfigurator{MongoDB: s, SecretClient: r.SecretClient}
	if status := r.ensureX509SecretAndCheckTLSType(ctx, certConfigurator, currentAgentAuthMode, log); !status.IsOK() {
		return r.updateStatus(ctx, s, status, log)
	}

	if status := ensureRoles(s.Spec.GetSecurity().Roles, conn, log); !status.IsOK() {
		return r.updateStatus(ctx, s, status, log)
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

	var automationAgentVersion string
	if architectures.IsRunningStaticArchitecture(s.Annotations) {
		// In case the Agent *is* overridden, its version will be merged into the StatefulSet. The merging process
		// happens after creating the StatefulSet definition.
		if !s.IsAgentImageOverridden() {
			automationAgentVersion, err = r.getAgentVersion(conn, conn.OpsManagerVersion().VersionString, false, log)
			if err != nil {
				log.Errorf("Impossible to get agent version, please override the agent image by providing a pod template")
				status := workflow.Failed(xerrors.Errorf("Failed to get agent version: %w", err))
				return r.updateStatus(ctx, s, status, log)
			}
		}
	}

	standaloneOpts := construct.StandaloneOptions(
		CertificateHash(pem.ReadHashFromSecret(ctx, r.SecretClient, s.Namespace, standaloneCertSecretName, databaseSecretPath, log)),
		CurrentAgentAuthMechanism(currentAgentAuthMode),
		PodEnvVars(podVars),
		WithVaultConfig(vaultConfig),
		WithAdditionalMongodConfig(s.Spec.GetAdditionalMongodConfig()),
		WithInitDatabaseNonStaticImage(images.ContainerImage(r.imageUrls, util.InitDatabaseImageUrlEnv, r.initDatabaseNonStaticImageVersion)),
		WithDatabaseNonStaticImage(images.ContainerImage(r.imageUrls, util.NonStaticDatabaseEnterpriseImage, r.databaseNonStaticImageVersion)),
		WithAgentImage(images.ContainerImage(r.imageUrls, architectures.MdbAgentImageRepo, automationAgentVersion)),
		WithMongodbImage(images.GetOfficialImage(r.imageUrls, s.Spec.Version, s.GetAnnotations())),
	)

	sts := construct.DatabaseStatefulSet(*s, standaloneOpts, log)

	workflowStatus := create.HandlePVCResize(ctx, r.client, &sts, log)
	if !workflowStatus.IsOK() {
		return r.updateStatus(ctx, s, workflowStatus, log)
	}

	lastSpec, err := s.GetLastSpec()
	if err != nil {
		lastSpec = &mdbv1.MongoDbSpec{}
	}

	status := workflow.RunInGivenOrder(publishAutomationConfigFirst(ctx, r.client, *s, lastSpec, standaloneOpts, log),
		func() workflow.Status {
			return r.updateOmDeployment(ctx, conn, s, sts, false, log).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			if err = create.DatabaseInKubernetes(ctx, r.client, *s, sts, standaloneOpts, log); err != nil {
				return workflow.Failed(xerrors.Errorf("Failed to create/update (Kubernetes reconciliation phase): %w", err))
			}

			if status := getStatefulSetStatus(ctx, sts.Namespace, sts.Name, r.client); !status.IsOK() {
				return status
			}

			log.Info("Updated StatefulSet for standalone")
			return workflow.OK()
		})

	if !status.IsOK() {
		return r.updateStatus(ctx, s, status, log)
	}

	annotationsToAdd, err := getAnnotationsForResource(s)
	if err != nil {
		return r.updateStatus(ctx, s, workflow.Failed(err), log)
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
	if err := annotations.SetAnnotations(ctx, s, annotationsToAdd, r.client); err != nil {
		return r.updateStatus(ctx, s, workflow.Failed(err), log)
	}

	log.Infof("Finished reconciliation for MongoDbStandalone! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return r.updateStatus(ctx, s, status, log, mdbstatus.NewBaseUrlOption(deployment.Link(conn.BaseURL(), conn.GroupID())))
}

func (r *ReconcileMongoDbStandalone) updateOmDeployment(ctx context.Context, conn om.Connection, s *mdbv1.MongoDB, set appsv1.StatefulSet, isRecovering bool, log *zap.SugaredLogger) workflow.Status {
	if err := agents.WaitForRsAgentsToRegister(set, 0, s.Spec.GetClusterDomain(), conn, log, s); err != nil {
		return workflow.Failed(err)
	}

	// TODO standalone PR
	status, additionalReconciliationRequired := r.updateOmAuthentication(ctx, conn, []string{set.Name}, s, "", "", "", isRecovering, log)
	if !status.IsOK() {
		return status
	}

	standaloneOmObject := createProcess(r.imageUrls[mcoConstruct.MongodbImageEnv], r.forceEnterprise, set, util.DatabaseContainerName, s)
	err := conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			excessProcesses := d.GetNumberOfExcessProcesses(s.Name)
			if excessProcesses > 0 {
				return xerrors.Errorf("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)")
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
		return workflow.Failed(err)
	}

	if err := om.WaitForReadyState(conn, []string{set.Name}, isRecovering, log); err != nil {
		return workflow.Failed(err)
	}

	if additionalReconciliationRequired {
		return workflow.Pending("Performing multi stage reconciliation")
	}

	log.Info("Updated Ops Manager for standalone")
	return workflow.OK()
}

func (r *ReconcileMongoDbStandalone) OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error {
	s := obj.(*mdbv1.MongoDB)

	log.Infow("Removing standalone from Ops Manager", "config", s.Spec)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.client, r.SecretClient, s, log)
	if err != nil {
		return err
	}

	conn, _, err := connection.PrepareOpsManagerConnection(ctx, r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, s.Namespace, log)
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
		return xerrors.Errorf("failed to update Ops Manager automation config: %w", err)
	}

	if err := om.WaitForReadyState(conn, processNames, false, log); err != nil {
		return err
	}

	hostsToRemove, _ := dns.GetDNSNames(s.Name, s.ServiceName(), s.Namespace, s.Spec.GetClusterDomain(), 1, nil)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err = host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}
	if err := r.clearProjectAuthenticationSettings(ctx, conn, s, processNames, log); err != nil {
		return err
	}

	r.resourceWatcher.RemoveDependentWatchedResources(s.ObjectKey())

	log.Infow("Clear feature control for group: %s", "groupID", conn.GroupID())
	if result := controlledfeature.ClearFeatureControls(conn, conn.OpsManagerVersion(), log); !result.IsOK() {
		result.Log(log)
		log.Warnf("Failed to clear feature control from group: %s", conn.GroupID())
	}

	log.Info("Removed standalone from Ops Manager!")
	return nil
}

func createProcess(mongoDBImage string, forceEnterprise bool, set appsv1.StatefulSet, containerName string, s *mdbv1.MongoDB) om.Process {
	hostnames, _ := dns.GetDnsForStatefulSet(set, s.Spec.GetClusterDomain(), nil)
	process := om.NewMongodProcess(s.Name, hostnames[0], mongoDBImage, forceEnterprise, s.Spec.GetAdditionalMongodConfig(), s.GetSpec(), "", s.Annotations, s.CalculateFeatureCompatibilityVersion())
	return process
}
