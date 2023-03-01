package operator

import (
	"context"
	"fmt"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault/vaultwatcher"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/replicaset"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"
	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileMongoDbShardedCluster
type ReconcileMongoDbShardedCluster struct {
	*ReconcileCommonController
	configSrvScaler       shardedClusterScaler
	mongosScaler          shardedClusterScaler
	mongodsPerShardScaler shardedClusterScaler
	omConnectionFactory   om.ConnectionFactory
}

func newShardedClusterReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *ReconcileMongoDbShardedCluster {
	return &ReconcileMongoDbShardedCluster{
		ReconcileCommonController: newReconcileCommonController(mgr),
		omConnectionFactory:       omFunc,
	}
}

func (r *ReconcileMongoDbShardedCluster) Reconcile(_ context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	agents.UpgradeAllIfNeeded(r.client, r.SecretClient, r.omConnectionFactory, GetWatchedNamespace())

	log := zap.S().With("ShardedCluster", request.NamespacedName)
	sc := &mdbv1.MongoDB{}

	reconcileResult, err := r.prepareResourceForReconciliation(request, sc, log)
	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcileResult, err
	}

	if err := sc.ProcessValidationsOnReconcile(nil); err != nil {
		return r.updateStatus(sc, workflow.Invalid(err.Error()), log)
	}

	r.initCountsForThisReconciliation(*sc)

	log.Info("-> ShardedCluster.Reconcile")
	log.Infow("ShardedCluster.Spec", "spec", sc.Spec)
	log.Infow("ShardedCluster.Status", "status", sc.Status)
	log.Infow("ShardedClusterScaling", "mongosScaler", r.mongosScaler, "configSrvScaler", r.configSrvScaler, "mongodsPerShardScaler", r.mongodsPerShardScaler, "desiredShards", sc.Spec.ShardCount, "currentShards", sc.Status.ShardCount)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, sc, log)
	if err != nil {
		return r.updateStatus(sc, workflow.Failed(err.Error()), log)
	}

	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, sc.Namespace, log)
	if err != nil {
		return r.updateStatus(sc, workflow.Failed(err.Error()), log)
	}

	status := r.doShardedClusterProcessing(sc, conn, projectConfig, log)
	if !status.IsOK() || status.Phase() == mdbstatus.PhaseUnsupported {
		return r.updateStatus(sc, status, log)
	}

	if scale.AnyAreStillScaling(r.mongodsPerShardScaler, r.configSrvScaler, r.mongosScaler) {
		return r.updateStatus(sc, workflow.Pending("Continuing scaling operation for ShardedCluster %s mongodsPerShardCount %+v, mongosCount %+v, configServerCount %+v",
			sc.ObjectKey(),
			r.mongodsPerShardScaler,
			r.mongosScaler,
			r.configSrvScaler,
		),
			log,
			mdbstatus.MongodsPerShardOption(r.mongodsPerShardScaler),
			mdbstatus.ConfigServerOption(r.configSrvScaler),
			mdbstatus.MongosCountOption(r.mongosScaler),
		)
	}

	// only remove any stateful sets if we are scaling down
	// Note: we should only remove unused stateful sets once we are fully complete
	// removing members 1 at a time.
	if sc.Spec.ShardCount < sc.Status.ShardCount {
		r.removeUnusedStatefulsets(sc, log)
	}

	annotationsToAdd, err := getAnnotationsForResource(sc)
	if err != nil {
		return r.updateStatus(sc, workflow.Failed(err.Error()), log)
	}

	if vault.IsVaultSecretBackend() {
		secrets := sc.GetSecretsMountedIntoDBPod()
		vaultMap := make(map[string]string)
		for _, s := range secrets {
			path := fmt.Sprintf("%s/%s/%s", r.VaultClient.DatabaseSecretMetadataPath(), sc.Namespace, s)
			vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		}
		path := fmt.Sprintf("%s/%s/%s", r.VaultClient.OperatorScretMetadataPath(), sc.Namespace, sc.Spec.Credentials)
		vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		for k, val := range vaultMap {
			annotationsToAdd[k] = val
		}
	}
	if err := annotations.SetAnnotations(sc.DeepCopy(), annotationsToAdd, r.client); err != nil {
		return r.updateStatus(sc, workflow.Failed(err.Error()), log)
	}

	log.Infof("Finished reconciliation for Sharded Cluster! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return r.updateStatus(sc, status, log, mdbstatus.NewBaseUrlOption(deployment.Link(conn.BaseURL(), conn.GroupID())),
		mdbstatus.MongodsPerShardOption(r.mongodsPerShardScaler), mdbstatus.ConfigServerOption(r.configSrvScaler), mdbstatus.MongosCountOption(r.mongosScaler))
}

func (r *ReconcileMongoDbShardedCluster) initCountsForThisReconciliation(sc mdbv1.MongoDB) {
	r.mongosScaler = shardedClusterScaler{CurrentMembers: sc.Status.MongosCount, DesiredMembers: sc.Spec.MongosCount}
	r.configSrvScaler = shardedClusterScaler{CurrentMembers: sc.Status.ConfigServerCount, DesiredMembers: sc.Spec.ConfigServerCount}
	r.mongodsPerShardScaler = shardedClusterScaler{CurrentMembers: sc.Status.MongodsPerShardCount, DesiredMembers: sc.Spec.MongodsPerShardCount}
}

func (r *ReconcileMongoDbShardedCluster) getConfigSrvCountThisReconciliation() int {
	return scale.ReplicasThisReconciliation(r.configSrvScaler)
}

func (r *ReconcileMongoDbShardedCluster) getMongosCountThisReconciliation() int {
	return scale.ReplicasThisReconciliation(r.mongosScaler)
}

func (r *ReconcileMongoDbShardedCluster) getMongodsPerShardCountThisReconciliation() int {
	return scale.ReplicasThisReconciliation(r.mongodsPerShardScaler)
}

// implements all the logic to do the sharded cluster thing
func (r *ReconcileMongoDbShardedCluster) doShardedClusterProcessing(obj interface{}, conn om.Connection, projectConfig mdbv1.ProjectConfig, log *zap.SugaredLogger) workflow.Status {
	log.Info("ShardedCluster.doShardedClusterProcessing")
	sc := obj.(*mdbv1.MongoDB)

	if status := ensureSupportedOpsManagerVersion(conn); status.Phase() != mdbstatus.PhaseRunning {
		return status
	}

	r.RemoveDependentWatchedResources(sc.ObjectKey())
	r.RegisterWatchedMongodbResources(sc.ObjectKey(), sc.Spec.GetProject(), sc.Spec.Credentials)

	// In case of Sharded Cluster we have to watch a bunch of different secrets
	if sc.GetSecurity().IsTLSEnabled() {
		secretNames := []string{}
		secretNames = append(secretNames,
			sc.GetSecurity().MemberCertificateSecretName(sc.MongosRsName()),
			sc.GetSecurity().MemberCertificateSecretName(sc.ConfigRsName()),
		)

		for i := 0; i < sc.Spec.ShardCount; i++ {
			secretNames = append(secretNames, sc.GetSecurity().MemberCertificateSecretName(sc.ShardRsName(i)))
		}
		r.RegisterWatchedTLSResources(sc.ObjectKey(), sc.Spec.GetTLSConfig().CA, secretNames)
	}

	reconcileResult := checkIfHasExcessProcesses(conn, sc, log)
	if !reconcileResult.IsOK() {
		return reconcileResult
	}

	security := sc.Spec.Security
	// TODO move to webhook validations
	if security.Authentication != nil && security.Authentication.Enabled && security.Authentication.IsX509Enabled() && !sc.Spec.GetSecurity().IsTLSEnabled() {
		return workflow.Invalid("cannot have a non-tls deployment when x509 authentication is enabled")
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return workflow.Failed(err.Error())
	}

	podEnvVars := newPodVars(conn, projectConfig, sc.Spec.ConnectionSpec)

	status, certSecretTypesForSTS := r.ensureSSLCertificates(sc, log)
	if !status.IsOK() {
		return status
	}

	prometheusCertHash, err := certs.EnsureTLSCertsForPrometheus(r.SecretClient, sc.GetNamespace(), sc.GetPrometheus(), certs.Database, log)
	if err != nil {
		return workflow.Failed("Could not generate certificates for Prometheus: %s", err)
	}

	opts := deploymentOptions{
		podEnvVars:           podEnvVars,
		currentAgentAuthMode: currentAgentAuthMode,
		certTLSType:          certSecretTypesForSTS,
	}

	if err = r.prepareScaleDownShardedCluster(conn, sc, opts, log); err != nil {
		return workflow.Failed("failed to perform scale down preliminary actions: %s", err)
	}

	if status := validateMongoDBResource(sc, conn); !status.IsOK() {
		return status
	}

	// Ensures that all sharded cluster certificates are either of Opaque type (old design)
	// or are all of kubernetes.io/tls type
	// and save the value for future use
	allCertsType, err := getCertTypeForAllShardedClusterCertificates(certSecretTypesForSTS)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	caFilePath := util.CAFilePathInContainer
	if allCertsType == corev1.SecretTypeTLS {
		caFilePath = fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)
	}

	if status := controlledfeature.EnsureFeatureControls(*sc, conn, conn.OpsManagerVersion(), log); !status.IsOK() {
		return status
	}

	certConfigurator := certs.ShardedSetX509CertConfigurator{
		MongoDB:               sc,
		MongodsPerShardScaler: r.mongodsPerShardScaler,
		MongosScaler:          r.mongosScaler,
		ConfigSrvScaler:       r.configSrvScaler,
		SecretClient:          r.SecretClient,
	}

	status = r.ensureX509SecretAndCheckTLSType(certConfigurator, currentAgentAuthMode, log)
	if !status.IsOK() {
		return status
	}

	if status := ensureRoles(sc.Spec.GetSecurity().Roles, conn, log); !status.IsOK() {
		return status
	}

	agentCertSecretName := sc.GetSecurity().AgentClientCertificateSecretName(sc.Name).Name

	opts = deploymentOptions{
		podEnvVars:           podEnvVars,
		currentAgentAuthMode: currentAgentAuthMode,
		caFilePath:           caFilePath,
		agentCertSecretName:  agentCertSecretName,
		prometheusCertHash:   prometheusCertHash,
	}
	allConfigs := r.getAllConfigs(*sc, opts, log)

	agentCertSecretName += certs.OperatorGeneratedCertSuffix

	status = workflow.RunInGivenOrder(anyStatefulSetNeedsToPublishState(*sc, r.client, allConfigs, log),
		func() workflow.Status {
			return r.updateOmDeploymentShardedCluster(conn, sc, opts, log).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			return r.createKubernetesResources(sc, opts, log).OnErrorPrepend("Failed to create/update (Kubernetes reconciliation phase):")
		})

	if !status.IsOK() {
		return status
	}
	return reconcileResult
}

// getCertTypeForAllShardedClusterCertificates checks whether all certificates secret are of the same type and returns it.
func getCertTypeForAllShardedClusterCertificates(certTypes map[string]bool) (corev1.SecretType, error) {
	if len(certTypes) == 0 {
		return corev1.SecretTypeTLS, nil
	}
	valueSlice := make([]bool, 0, len(certTypes))
	for _, v := range certTypes {
		valueSlice = append(valueSlice, v)
	}
	curTypeIsTLS := valueSlice[0]
	for i := 1; i < len(valueSlice); i++ {
		if valueSlice[i] != curTypeIsTLS {
			return corev1.SecretTypeOpaque, fmt.Errorf("TLS Certificates for Sharded cluster must all be of the same type - either kubernetes.io/tls or secrets containing a concatenated pem file")
		}
	}
	if curTypeIsTLS {
		return corev1.SecretTypeTLS, nil
	}
	return corev1.SecretTypeOpaque, nil
}

// anyStatefulSetNeedsToPublishState checks to see if any stateful set
// of the given sharded cluster needs to publish state to Ops Manager before updating Kubernetes resources
func anyStatefulSetNeedsToPublishState(sc mdbv1.MongoDB, getter ConfigMapStatefulSetSecretGetter, configs []func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) bool {
	for _, cf := range configs {
		if needToPublishStateFirst(getter, sc, cf, log) {
			return true
		}
	}
	return false
}

// getAllConfigs returns a list of all the configuration functions associated with the Sharded Cluster.
// This includes the Mongos, the Config Server and all Shards
func (r ReconcileMongoDbShardedCluster) getAllConfigs(sc mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) []func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	allConfigs := make([]func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, 0)
	for i := 0; i < sc.Spec.ShardCount; i++ {
		allConfigs = append(allConfigs, r.getShardOptions(sc, i, opts, log))
	}
	allConfigs = append(allConfigs, r.getConfigServerOptions(sc, opts, log))
	allConfigs = append(allConfigs, r.getMongosOptions(sc, opts, log))
	return allConfigs
}

func (r *ReconcileMongoDbShardedCluster) removeUnusedStatefulsets(sc *mdbv1.MongoDB, log *zap.SugaredLogger) {
	statefulsetsToRemove := sc.Status.ShardCount - sc.Spec.ShardCount
	shardsCount := sc.Status.MongodbShardedClusterSizeConfig.ShardCount

	// we iterate over last 'statefulsetsToRemove' shards if any
	for i := shardsCount - statefulsetsToRemove; i < shardsCount; i++ {
		key := kube.ObjectKey(sc.Namespace, sc.ShardRsName(i))
		err := r.client.DeleteStatefulSet(key)
		if err != nil {
			// Most of all the error won't be recoverable, also our sharded cluster is in good shape - we can just warn
			// the error and leave the cleanup work for the admins
			log.Warnf("Failed to delete the statefulset %s: %s", key, err)
		}
		log.Infof("Removed statefulset %s as it's was removed from sharded cluster", key)
	}
}

func (r *ReconcileMongoDbShardedCluster) ensureSSLCertificates(s *mdbv1.MongoDB, log *zap.SugaredLogger) (workflow.Status, map[string]bool) {
	tlsConfig := s.Spec.GetTLSConfig()

	certSecretTypes := map[string]bool{}
	if tlsConfig == nil || !s.Spec.GetSecurity().IsTLSEnabled() {
		return workflow.OK(), certSecretTypes
	}

	var status workflow.Status
	status = workflow.OK()
	mongosCert := certs.MongosConfig(*s, r.mongosScaler)
	tStatus := certs.EnsureSSLCertsForStatefulSet(r.SecretClient, r.SecretClient, *s.Spec.Security, mongosCert, log)
	certSecretTypes[mongosCert.CertSecretName] = true
	status = status.Merge(tStatus)

	configSrvCert := certs.ConfigSrvConfig(*s, r.configSrvScaler)
	tStatus = certs.EnsureSSLCertsForStatefulSet(r.SecretClient, r.SecretClient, *s.Spec.Security, configSrvCert, log)
	certSecretTypes[configSrvCert.CertSecretName] = true
	status = status.Merge(tStatus)

	for i := 0; i < s.Spec.ShardCount; i++ {
		shardCert := certs.ShardConfig(*s, i, r.mongodsPerShardScaler)
		tStatus := certs.EnsureSSLCertsForStatefulSet(r.SecretClient, r.SecretClient, *s.Spec.Security, shardCert, log)
		certSecretTypes[shardCert.CertSecretName] = true
		status = status.Merge(tStatus)
	}

	return status, certSecretTypes
}

// createKubernetesResources creates all Kubernetes objects that are specified in 'state' parameter.
// This function returns errorStatus if any errors occured or pendingStatus if the statefulsets are not
// ready yet
// Note, that it doesn't remove any existing shards - this will be done later
func (r *ReconcileMongoDbShardedCluster) createKubernetesResources(s *mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) workflow.Status {
	configSrvOpts := r.getConfigServerOptions(*s, opts, log)
	configSrvSts := construct.DatabaseStatefulSet(*s, configSrvOpts, nil)
	if err := create.DatabaseInKubernetes(r.client, *s, configSrvSts, configSrvOpts, log); err != nil {
		return workflow.Failed("Failed to create Config Server Stateful Set: %s", err)
	}
	if status := getStatefulSetStatus(s.Namespace, s.ConfigRsName(), r.client); !status.IsOK() {
		return status
	}
	_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)

	log.Infow("Created/updated StatefulSet for config servers", "name", s.ConfigRsName(), "servers count", configSrvSts.Spec.Replicas)

	shardsNames := make([]string, s.Spec.ShardCount)

	for i := 0; i < s.Spec.ShardCount; i++ {
		shardsNames[i] = s.ShardRsName(i)
		shardOpts := r.getShardOptions(*s, i, opts, log)
		shardSts := construct.DatabaseStatefulSet(*s, shardOpts, nil)

		if err := create.DatabaseInKubernetes(r.client, *s, shardSts, shardOpts, log); err != nil {
			return workflow.Failed("Failed to create Stateful Set for shard %s: %s", shardsNames[i], err)
		}
		if status := getStatefulSetStatus(s.Namespace, shardsNames[i], r.client); !status.IsOK() {
			return status
		}
		_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)
	}

	log.Infow("Created/updated Stateful Sets for shards in Kubernetes", "shards", shardsNames)

	mongosOpts := r.getMongosOptions(*s, opts, log)
	mongosSts := construct.DatabaseStatefulSet(*s, mongosOpts, nil)

	if err := create.DatabaseInKubernetes(r.client, *s, mongosSts, mongosOpts, log); err != nil {
		return workflow.Failed("Failed to create Mongos Stateful Set: %s", err)
	}

	if status := getStatefulSetStatus(s.Namespace, s.MongosRsName(), r.client); !status.IsOK() {
		return status
	}
	_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)

	log.Infow("Created/updated StatefulSet for mongos servers", "name", s.MongosRsName(), "servers count", mongosSts.Spec.Replicas)
	return workflow.OK()
}

// OnDelete tries to complete a Deletion reconciliation event
func (r *ReconcileMongoDbShardedCluster) OnDelete(obj runtime.Object, log *zap.SugaredLogger) error {
	sc := obj.(*mdbv1.MongoDB)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, sc, log)
	if err != nil {
		return err
	}

	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, sc.Namespace, log)
	if err != nil {
		return err
	}

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			processNames = d.GetProcessNames(om.ShardedCluster{}, sc.Name)
			if e := d.RemoveShardedClusterByName(sc.Name, log); e != nil {
				log.Warnf("Failed to remove sharded cluster from automation config: %s", e)
			}
			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return err
	}

	if sc.Spec.Backup != nil && sc.Spec.Backup.AutoTerminateOnDeletion {
		if err := backup.StopBackupIfEnabled(conn, conn, sc.Name, backup.ReplicaSetType, log); err != nil {
			return err
		}
	}

	currentCount := mdbv1.MongodbShardedClusterSizeConfig{
		MongodsPerShardCount: sc.Status.MongodsPerShardCount,
		MongosCount:          sc.Status.MongosCount,
		ShardCount:           sc.Status.ShardCount,
		ConfigServerCount:    sc.Status.ConfigServerCount,
	}

	desiredCountThisReconciliation := mdbv1.MongodbShardedClusterSizeConfig{
		MongodsPerShardCount: r.getMongodsPerShardCountThisReconciliation(),
		MongosCount:          r.getMongosCountThisReconciliation(),
		ShardCount:           sc.Spec.ShardCount,
		ConfigServerCount:    r.getConfigSrvCountThisReconciliation(),
	}

	sizeConfig := getMaxShardedClusterSizeConfig(desiredCountThisReconciliation, currentCount)
	hostsToRemove := getAllHosts(sc, sizeConfig)
	log.Infow("Stop monitoring removed hosts in Ops Manager", "hostsToBeRemoved", hostsToRemove)

	if err = host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	if err := r.clearProjectAuthenticationSettings(conn, sc, processNames, log); err != nil {
		return err
	}

	r.RemoveDependentWatchedResources(sc.ObjectKey())

	log.Info("Removed sharded cluster from Ops Manager!")

	return nil
}

func AddShardedClusterController(mgr manager.Manager) error {
	reconciler := newShardedClusterReconciler(mgr, om.NewOpsManagerConnection)
	options := controller.Options{Reconciler: reconciler}
	c, err := controller.New(util.MongoDbShardedClusterController, mgr, options)
	if err != nil {
		return err
	}

	// watch for changes to sharded cluster MongoDB resources
	eventHandler := ResourceEventHandler{deleter: reconciler}
	err = c.Watch(&source.Kind{Type: &mdbv1.MongoDB{}}, &eventHandler, watch.PredicatesForMongoDB(mdbv1.ShardedCluster))
	if err != nil {
		return err
	}

	// TODO CLOUDP-35240
	/*err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
	  	IsController: true,
	  	OwnerType:    &mdbv1.MongoDbShardedCluster{},
	  })
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
		go vaultwatcher.WatchSecretChangeForMDB(zap.S(), eventChannel, reconciler.client, reconciler.VaultClient, mdbv1.ShardedCluster)

		err = c.Watch(
			&source.Channel{Source: eventChannel},
			&handler.EnqueueRequestForObject{},
		)
		if err != nil {
			zap.S().Errorf("Failed to watch for vault secret changes: %w", err)
		}
	}
	zap.S().Infof("Registered controller %s", util.MongoDbShardedClusterController)

	return nil
}

func (r *ReconcileMongoDbShardedCluster) prepareScaleDownShardedCluster(omClient om.Connection, sc *mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) error {
	membersToScaleDown := make(map[string][]string)
	clusterName := sc.Spec.GetClusterDomain()

	// Scaledown amount of replicas in ConfigServer
	if r.isConfigServerScaleDown() {
		sts := construct.DatabaseStatefulSet(*sc, r.getConfigServerOptions(*sc, opts, log), nil)
		_, podNames := dns.GetDnsForStatefulSetReplicasSpecified(sts, clusterName, sc.Status.ConfigServerCount, nil)
		membersToScaleDown[sc.ConfigRsName()] = podNames[r.getConfigSrvCountThisReconciliation():sc.Status.ConfigServerCount]
	}

	// Scaledown size of each shard
	if r.isShardsSizeScaleDown() {
		for i := 0; i < sc.Spec.ShardCount; i++ {
			sts := construct.DatabaseStatefulSet(*sc, r.getShardOptions(*sc, i, opts, log), nil)
			_, podNames := dns.GetDnsForStatefulSetReplicasSpecified(sts, clusterName, sc.Status.MongodsPerShardCount, nil)
			membersToScaleDown[sc.ShardRsName(i)] = podNames[r.getMongodsPerShardCountThisReconciliation():sc.Status.MongodsPerShardCount]
		}
	}

	if len(membersToScaleDown) > 0 {
		if err := replicaset.PrepareScaleDownFromMap(omClient, membersToScaleDown, log); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileMongoDbShardedCluster) isConfigServerScaleDown() bool {
	return scale.ReplicasThisReconciliation(r.configSrvScaler) < r.configSrvScaler.CurrentReplicas()
}

func (r *ReconcileMongoDbShardedCluster) isShardsSizeScaleDown() bool {
	return scale.ReplicasThisReconciliation(r.mongodsPerShardScaler) < r.mongodsPerShardScaler.CurrentReplicas()
}

// deploymentOptions contains fields required for creating the OM deployment for the Sharded Cluster.
type deploymentOptions struct {
	podEnvVars           *env.PodEnvVars
	currentAgentAuthMode string
	caFilePath           string
	agentCertSecretName  string
	certTLSType          map[string]bool
	finalizing           bool
	processNames         []string
	prometheusCertHash   string
}

// updateOmDeploymentShardedCluster performs OM registration operation for the sharded cluster. So the changes will be finally propagated
// to automation agents in containers
// Note that the process may have two phases (if shards number is decreased):
// phase 1: "drain" the shards: remove them from sharded cluster, put replica set names to "draining" array, not remove
// replica sets and processes, wait for agents to reach the goal
// phase 2: remove the "junk" replica sets and their processes, wait for agents to reach the goal.
// The logic is designed to be idempotent: if the reconciliation is retried the controller will never skip the phase 1
// until the agents have performed draining
func (r *ReconcileMongoDbShardedCluster) updateOmDeploymentShardedCluster(conn om.Connection, sc *mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) workflow.Status {
	err := r.waitForAgentsToRegister(sc, conn, opts, log, sc)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	dep, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err.Error())
	}

	opts.finalizing = false
	opts.processNames = dep.GetProcessNames(om.ShardedCluster{}, sc.Name)

	processNames, shardsRemoving, status := r.publishDeployment(conn, sc, &opts, log)

	if !status.IsOK() {
		return status
	}

	if err = om.WaitForReadyState(conn, processNames, log); err != nil {
		if shardsRemoving {
			return workflow.Pending("automation agents haven't reached READY state: shards removal in progress")
		}
		return workflow.Failed(err.Error())
	}

	if shardsRemoving {
		opts.finalizing = true

		log.Infof("Some shards were removed from the sharded cluster, we need to remove them from the deployment completely")
		processNames, _, status = r.publishDeployment(conn, sc, &opts, log)
		if !status.IsOK() {
			return status
		}

		if err = om.WaitForReadyState(conn, processNames, log); err != nil {
			return workflow.Failed("automation agents haven't reached READY state while cleaning replica set and processes: %s", err)
		}
	}

	currentCount := mdbv1.MongodbShardedClusterSizeConfig{
		MongodsPerShardCount: sc.Status.MongodsPerShardCount,
		MongosCount:          sc.Status.MongosCount,
		ShardCount:           sc.Status.ShardCount,
		ConfigServerCount:    sc.Status.ConfigServerCount,
	}

	desiredCount := mdbv1.MongodbShardedClusterSizeConfig{
		MongodsPerShardCount: r.getMongodsPerShardCountThisReconciliation(),
		MongosCount:          r.getMongosCountThisReconciliation(),
		ShardCount:           sc.Spec.ShardCount,
		ConfigServerCount:    r.getConfigSrvCountThisReconciliation(),
	}

	currentHosts := getAllHosts(sc, currentCount)
	wantedHosts := getAllHosts(sc, desiredCount)

	if err = host.CalculateDiffAndStopMonitoring(conn, currentHosts, wantedHosts, log); err != nil {
		return workflow.Failed(err.Error())
	}

	if status := r.ensureBackupConfigurationAndUpdateStatus(conn, sc, r.SecretClient, log); !status.IsOK() {
		return status
	}

	log.Info("Updated Ops Manager for sharded cluster")
	return workflow.OK()
}

func (r *ReconcileMongoDbShardedCluster) publishDeployment(conn om.Connection, sc *mdbv1.MongoDB, opts *deploymentOptions, log *zap.SugaredLogger) ([]string, bool, workflow.Status) {

	// mongos
	sts := construct.DatabaseStatefulSet(*sc, r.getMongosOptions(*sc, *opts, log), nil)
	mongosInternalClusterPath := statefulset.GetFilePathFromAnnotationOrDefault(sts, util.InternalCertAnnotationKey, util.InternalClusterAuthMountPath, "")
	mongosMemberCertPath := statefulset.GetFilePathFromAnnotationOrDefault(sts, certs.CertHashAnnotationkey, util.TLSCertMountPath, util.PEMKeyFilePathInContainer)
	mongosProcesses := createMongosProcesses(sts, sc, mongosMemberCertPath)

	// config server
	configSvrSts := construct.DatabaseStatefulSet(*sc, r.getConfigServerOptions(*sc, *opts, log), nil)
	configInternalClusterPath := statefulset.GetFilePathFromAnnotationOrDefault(configSvrSts, util.InternalCertAnnotationKey, util.InternalClusterAuthMountPath, "")
	configMemberCertPath := statefulset.GetFilePathFromAnnotationOrDefault(configSvrSts, certs.CertHashAnnotationkey, util.TLSCertMountPath, util.PEMKeyFilePathInContainer)
	configRs := buildReplicaSetFromProcesses(configSvrSts.Name, createConfigSrvProcesses(configSvrSts, sc, configMemberCertPath), sc)

	// shards
	shards := make([]om.ReplicaSetWithProcesses, sc.Spec.ShardCount)
	shardsInternalClusterPath := make([]string, len(shards))
	for i := 0; i < sc.Spec.ShardCount; i++ {
		shardSts := construct.DatabaseStatefulSet(*sc, r.getShardOptions(*sc, i, *opts, log), nil)
		shardsInternalClusterPath[i] = statefulset.GetFilePathFromAnnotationOrDefault(shardSts, util.InternalCertAnnotationKey, util.InternalClusterAuthMountPath, "")
		shardMemberCertPath := statefulset.GetFilePathFromAnnotationOrDefault(shardSts, certs.CertHashAnnotationkey, util.TLSCertMountPath, util.PEMKeyFilePathInContainer)
		shards[i] = buildReplicaSetFromProcesses(shardSts.Name, createShardProcesses(shardSts, sc, shardMemberCertPath), sc)
	}

	status, additionalReconciliationRequired := r.updateOmAuthentication(conn, opts.processNames, sc, opts.agentCertSecretName, opts.caFilePath, log)
	if !status.IsOK() {
		return nil, false, status
	}

	var finalProcesses []string
	shardsRemoving := false
	err := conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			// it is not possible to disable internal cluster authentication once enabled
			allProcesses := getAllProcesses(shards, configRs, mongosProcesses)
			if sc.Spec.Security.GetInternalClusterAuthenticationMode() == "" && d.ExistingProcessesHaveInternalClusterAuthentication(allProcesses) {
				return fmt.Errorf("cannot disable x509 internal cluster authentication")
			}
			numberOfOtherMembers := d.GetNumberOfExcessProcesses(sc.Name)
			if numberOfOtherMembers > 0 {
				return fmt.Errorf("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)")
			}

			lastConfigServerConf, err := sc.GetLastAdditionalMongodConfigByType(mdbv1.ConfigServerConfig)
			if err != nil {
				return err
			}

			lastShardServerConf, err := sc.GetLastAdditionalMongodConfigByType(mdbv1.ShardConfig)
			if err != nil {
				return err
			}

			lastMongosServerConf, err := sc.GetLastAdditionalMongodConfigByType(mdbv1.MongosConfig)
			if err != nil {
				return err
			}

			mergeOpts := om.DeploymentShardedClusterMergeOptions{
				Name:                                 sc.Name,
				MongosProcesses:                      mongosProcesses,
				ConfigServerRs:                       configRs,
				Shards:                               shards,
				Finalizing:                           opts.finalizing,
				ConfigServerAdditionalOptionsPrev:    lastConfigServerConf.ToMap(),
				ConfigServerAdditionalOptionsDesired: sc.Spec.ConfigSrvSpec.AdditionalMongodConfig.ToMap(),
				ShardAdditionalOptionsPrev:           lastShardServerConf.ToMap(),
				ShardAdditionalOptionsDesired:        sc.Spec.ShardSpec.AdditionalMongodConfig.ToMap(),
				MongosAdditionalOptionsPrev:          lastMongosServerConf.ToMap(),
				MongosAdditionalOptionsDesired:       sc.Spec.MongosSpec.AdditionalMongodConfig.ToMap(),
			}

			if shardsRemoving, err = d.MergeShardedCluster(mergeOpts); err != nil {
				return err
			}

			d.AddMonitoringAndBackup(log, sc.Spec.GetSecurity().IsTLSEnabled(), opts.caFilePath)
			d.ConfigureTLS(sc.Spec.GetSecurity(), opts.caFilePath)

			internalClusterAuthMode := sc.Spec.Security.GetInternalClusterAuthenticationMode()

			d.ConfigureInternalClusterAuthentication(d.GetShardedClusterConfigProcessNames(sc.Name), internalClusterAuthMode, configInternalClusterPath)
			d.ConfigureInternalClusterAuthentication(d.GetShardedClusterMongosProcessNames(sc.Name), internalClusterAuthMode, mongosInternalClusterPath)

			_ = UpdatePrometheus(&d, conn, sc.GetPrometheus(), r.SecretClient, sc.GetNamespace(), opts.prometheusCertHash, log)

			for i, path := range shardsInternalClusterPath {
				d.ConfigureInternalClusterAuthentication(d.GetShardedClusterShardProcessNames(sc.Name, i), internalClusterAuthMode, path)
			}

			finalProcesses = d.GetProcessNames(om.ShardedCluster{}, sc.Name)

			return nil
		},
		log,
	)

	if err != nil {
		return nil, shardsRemoving, workflow.Failed(err.Error())
	}

	if err := om.WaitForReadyState(conn, opts.processNames, log); err != nil {
		return nil, shardsRemoving, workflow.Failed(err.Error())
	}

	if additionalReconciliationRequired {
		return nil, shardsRemoving, workflow.Pending("Performing multi stage reconciliation")
	}

	return finalProcesses, shardsRemoving, workflow.OK()
}

func getAllProcesses(shards []om.ReplicaSetWithProcesses, configRs om.ReplicaSetWithProcesses, mongosProcesses []om.Process) []om.Process {
	allProcesses := make([]om.Process, 0)
	for _, shard := range shards {
		allProcesses = append(allProcesses, shard.Processes...)
	}
	allProcesses = append(allProcesses, configRs.Processes...)
	allProcesses = append(allProcesses, mongosProcesses...)
	return allProcesses
}

func (r *ReconcileMongoDbShardedCluster) waitForAgentsToRegister(sc *mdbv1.MongoDB, conn om.Connection, opts deploymentOptions,
	log *zap.SugaredLogger, mdb *mdbv1.MongoDB) error {

	mongosStatefulSet := construct.DatabaseStatefulSet(*sc, r.getMongosOptions(*sc, opts, log), nil)
	if err := agents.WaitForRsAgentsToRegister(mongosStatefulSet, 0, sc.Spec.GetClusterDomain(), conn, log, mdb); err != nil {
		return err
	}

	configSrvStatefulSet := construct.DatabaseStatefulSet(*sc, r.getConfigServerOptions(*sc, opts, log), nil)
	if err := agents.WaitForRsAgentsToRegister(configSrvStatefulSet, 0, sc.Spec.GetClusterDomain(), conn, log, mdb); err != nil {
		return err
	}

	for i := 0; i < sc.Spec.ShardCount; i++ {
		shardStatefulSet := construct.DatabaseStatefulSet(*sc, r.getShardOptions(*sc, i, opts, log), nil)
		if err := agents.WaitForRsAgentsToRegister(shardStatefulSet, 0, sc.Spec.GetClusterDomain(), conn, log, mdb); err != nil {
			return err
		}
	}
	return nil
}

func getMaxShardedClusterSizeConfig(specConfig mdbv1.MongodbShardedClusterSizeConfig, statusConfig mdbv1.MongodbShardedClusterSizeConfig) mdbv1.MongodbShardedClusterSizeConfig {
	return mdbv1.MongodbShardedClusterSizeConfig{
		MongosCount:          util.MaxInt(specConfig.MongosCount, statusConfig.MongosCount),
		ConfigServerCount:    util.MaxInt(specConfig.ConfigServerCount, statusConfig.ConfigServerCount),
		MongodsPerShardCount: util.MaxInt(specConfig.MongodsPerShardCount, statusConfig.MongodsPerShardCount),
		ShardCount:           util.MaxInt(specConfig.ShardCount, statusConfig.ShardCount),
	}
}

// getAllHostsFromStatus calculates a list of hosts from the "Status" of the Sharded Cluster
func getAllHosts(c *mdbv1.MongoDB, sizeConfig mdbv1.MongodbShardedClusterSizeConfig) []string {
	ans := make([]string, 0)

	hosts, _ := dns.GetDNSNames(c.MongosRsName(), c.ServiceName(), c.Namespace, c.Spec.GetClusterDomain(), sizeConfig.MongosCount, nil)
	ans = append(ans, hosts...)

	hosts, _ = dns.GetDNSNames(c.ConfigRsName(), c.ConfigSrvServiceName(), c.Namespace, c.Spec.GetClusterDomain(), sizeConfig.ConfigServerCount, nil)
	ans = append(ans, hosts...)

	for i := 0; i < sizeConfig.ShardCount; i++ {
		hosts, _ = dns.GetDNSNames(c.ShardRsName(i), c.ShardServiceName(), c.Namespace, c.Spec.GetClusterDomain(), sizeConfig.MongodsPerShardCount, nil)
		ans = append(ans, hosts...)
	}
	return ans
}

func createMongosProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB, certificateFilePath string) []om.Process {
	hostnames, names := dns.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain(), nil)
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongosProcess(names[idx], hostname, mdb.Spec.MongosSpec.GetAdditionalMongodConfig(), mdb.GetSpec(), certificateFilePath)
	}

	return processes
}
func createConfigSrvProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB, certificateFilePath string) []om.Process {
	return createMongodProcessForShardedCluster(set, mdb.Spec.ConfigSrvSpec.GetAdditionalMongodConfig(), mdb, certificateFilePath)
}
func createShardProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB, certificateFilePath string) []om.Process {
	return createMongodProcessForShardedCluster(set, mdb.Spec.ShardSpec.GetAdditionalMongodConfig(), mdb, certificateFilePath)
}
func createMongodProcessForShardedCluster(set appsv1.StatefulSet, additionalMongodConfig *mdbv1.AdditionalMongodConfig, mdb *mdbv1.MongoDB, certificateFilePath string) []om.Process {
	hostnames, names := dns.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain(), nil)
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(idx, names[idx], hostname, additionalMongodConfig, &mdb.Spec, certificateFilePath)
	}

	return processes
}

// buildReplicaSetFromProcesses creates the 'ReplicaSetWithProcesses' with specified processes. This is of use only
// for sharded cluster (config server, shards)
func buildReplicaSetFromProcesses(name string, members []om.Process, mdb *mdbv1.MongoDB) om.ReplicaSetWithProcesses {
	replicaSet := om.NewReplicaSet(name, mdb.Spec.GetMongoDBVersion())
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members)
	rsWithProcesses.SetHorizons(mdb.Spec.Connectivity.ReplicaSetHorizons)
	return rsWithProcesses
}

// shardedClusterScaler keeps track of each individual value being scaled on the sharded cluster
// and ensures these values are only incremented or decremented by one
type shardedClusterScaler struct {
	DesiredMembers int
	CurrentMembers int
}

func (r shardedClusterScaler) ForcedIndividualScaling() bool {
	return false
}

func (r shardedClusterScaler) DesiredReplicas() int {
	return r.DesiredMembers
}

func (r shardedClusterScaler) CurrentReplicas() int {
	return r.CurrentMembers
}

// getConfigServerOptions returns the Options needed to build the StatefulSet for the config server.
func (r *ReconcileMongoDbShardedCluster) getConfigServerOptions(sc mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	certSecretName := sc.GetSecurity().MemberCertificateSecretName(sc.ConfigRsName())
	internalClusterSecretName := sc.GetSecurity().InternalClusterAuthSecretName(sc.ConfigRsName())

	var vaultConfig vault.VaultConfiguration
	var databaseSecretPath string
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
		databaseSecretPath = r.VaultClient.DatabaseSecretPath()
	}

	return construct.ConfigServerOptions(
		Replicas(r.getConfigSrvCountThisReconciliation()),
		PodEnvVars(opts.podEnvVars),
		CurrentAgentAuthMechanism(opts.currentAgentAuthMode),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.SecretClient, sc.Namespace, certSecretName, databaseSecretPath, log)),
		InternalClusterHash(enterprisepem.ReadHashFromSecret(r.SecretClient, sc.Namespace, internalClusterSecretName, databaseSecretPath, log)),
		PrometheusTLSCertHash(opts.prometheusCertHash),
		WithVaultConfig(vaultConfig),
	)
}

// getMongosOptions returns the Options needed to build the StatefulSet for the mongos.
func (r *ReconcileMongoDbShardedCluster) getMongosOptions(sc mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	certSecretName := sc.GetSecurity().MemberCertificateSecretName(sc.MongosRsName())
	internalClusterSecretName := sc.GetSecurity().InternalClusterAuthSecretName(sc.MongosRsName())

	var vaultConfig vault.VaultConfiguration
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
	}
	return construct.MongosOptions(
		Replicas(r.getMongosCountThisReconciliation()),
		PodEnvVars(opts.podEnvVars),
		CurrentAgentAuthMechanism(opts.currentAgentAuthMode),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.SecretClient, sc.Namespace, certSecretName, vaultConfig.DatabaseSecretPath, log)),
		InternalClusterHash(enterprisepem.ReadHashFromSecret(r.SecretClient, sc.Namespace, internalClusterSecretName, vaultConfig.DatabaseSecretPath, log)),
		PrometheusTLSCertHash(opts.prometheusCertHash),
		WithVaultConfig(vaultConfig),
	)
}

// getShardOptions returns the Options needed to build the StatefulSet for a given shard.
func (r *ReconcileMongoDbShardedCluster) getShardOptions(sc mdbv1.MongoDB, shardNum int, opts deploymentOptions, log *zap.SugaredLogger) func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	certSecretName := sc.GetSecurity().MemberCertificateSecretName(sc.ShardRsName(shardNum))
	internalClusterSecretName := sc.GetSecurity().InternalClusterAuthSecretName(sc.ShardRsName(shardNum))

	var vaultConfig vault.VaultConfiguration
	var databaseSecretPath string
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
		databaseSecretPath = r.VaultClient.DatabaseSecretPath()
	}
	return construct.ShardOptions(shardNum,
		Replicas(r.getMongodsPerShardCountThisReconciliation()),
		PodEnvVars(opts.podEnvVars),
		CurrentAgentAuthMechanism(opts.currentAgentAuthMode),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.SecretClient, sc.Namespace, certSecretName, databaseSecretPath, log)),
		InternalClusterHash(enterprisepem.ReadHashFromSecret(r.SecretClient, sc.Namespace, internalClusterSecretName, databaseSecretPath, log)),
		PrometheusTLSCertHash(opts.prometheusCertHash),
		WithVaultConfig(vaultConfig),
	)
}
