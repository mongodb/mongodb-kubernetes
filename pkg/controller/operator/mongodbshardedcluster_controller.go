package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/certs"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/connection"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/controlledfeature"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/scale"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/host"
	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileMongoDbShardedCluster
type ReconcileMongoDbShardedCluster struct {
	*ReconcileCommonController
	watch.ResourceWatcher
	configSrvScaler       shardedClusterScaler
	mongosScaler          shardedClusterScaler
	mongodsPerShardScaler shardedClusterScaler
	omConnectionFactory   om.ConnectionFactory
}

func newShardedClusterReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *ReconcileMongoDbShardedCluster {
	return &ReconcileMongoDbShardedCluster{
		ReconcileCommonController: newReconcileCommonController(mgr),
		ResourceWatcher:           watch.NewResourceWatcher(),
		omConnectionFactory:       omFunc,
	}
}

func (r *ReconcileMongoDbShardedCluster) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	agents.UpgradeAllIfNeeded(r.kubeHelper.client, r.omConnectionFactory, getWatchedNamespace())

	log := zap.S().With("ShardedCluster", request.NamespacedName)
	sc := &mdbv1.MongoDB{}

	mutex := r.GetMutex(request.NamespacedName)
	mutex.Lock()
	defer mutex.Unlock()

	reconcileResult, err := r.prepareResourceForReconciliation(request, sc, log)
	if reconcileResult != nil {
		return *reconcileResult, err
	}

	if err := sc.ProcessValidationsOnReconcile(nil); err != nil {
		return r.updateStatus(sc, workflow.Invalid(err.Error()), log)
	}

	r.initCountsForThisReconciliation(*sc)

	log.Info("-> ShardedCluster.Reconcile")
	log.Infow("ShardedCluster.Spec", "spec", sc.Spec)
	log.Infow("ShardedCluster.Status", "status", sc.Status)
	log.Infow("ShardedClusterScaling", "mongosScaler", r.mongosScaler, "configSrvScaler", r.configSrvScaler, "mongodsPerShardScaler", r.mongodsPerShardScaler, "desiredShards", sc.Spec.ShardCount, "currentShards", sc.Status.ShardCount)

	conn, status := r.doShardedClusterProcessing(sc, log)
	if !status.IsOK() {
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
			scale.MongodsPerShardOption(r.mongodsPerShardScaler),
			scale.ConfigServerOption(r.configSrvScaler),
			scale.MongosCountOption(r.mongosScaler),
		)
	}

	// only remove any stateful sets if we are scaling down
	// Note: we should only remove unused stateful sets once we are fully complete
	// removing members 1 at a time.
	if sc.Spec.ShardCount < sc.Status.ShardCount {
		r.removeUnusedStatefulsets(sc, log)
	}

	log.Infof("Finished reconciliation for Sharded Cluster! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return r.updateStatus(sc, status, log, mdbstatus.NewBaseUrlOption(DeploymentLink(conn.BaseURL(), conn.GroupID())),
		scale.MongodsPerShardOption(r.mongodsPerShardScaler), scale.ConfigServerOption(r.configSrvScaler), scale.MongosCountOption(r.mongosScaler))
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
func (r *ReconcileMongoDbShardedCluster) doShardedClusterProcessing(obj interface{}, log *zap.SugaredLogger) (om.Connection, workflow.Status) {
	log.Info("ShardedCluster.doShardedClusterProcessing")
	sc := obj.(*mdbv1.MongoDB)

	projectConfig, credsConfig, err := readProjectConfigAndCredentials(r.kubeHelper.client, *sc)
	if err != nil {
		return nil, workflow.Failed(err.Error())
	}

	conn, err := connection.PrepareOpsManagerConnection(r.kubeHelper.client, projectConfig, credsConfig, r.omConnectionFactory, sc.Namespace, log)
	if err != nil {
		return nil, workflow.Failed(err.Error())
	}
	r.RegisterWatchedResources(sc.ObjectKey(), sc.Spec.GetProject(), sc.Spec.Credentials)

	reconcileResult := checkIfHasExcessProcesses(conn, sc, log)
	if !reconcileResult.IsOK() {
		return nil, reconcileResult
	}

	security := sc.Spec.Security
	// TODO move to webhook validations
	if security.Authentication != nil && security.Authentication.Enabled && security.Authentication.IsX509Enabled() && !sc.Spec.GetTLSConfig().Enabled {
		return nil, workflow.Invalid("cannot have a non-tls deployment when x509 authentication is enabled")
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return nil, workflow.Failed(err.Error())
	}

	podEnvVars := newPodVars(conn, projectConfig, sc.Spec.ConnectionSpec)
	kubeState := r.buildKubeObjectsForShardedCluster(sc, podEnvVars, projectConfig, currentAgentAuthMode, log)

	if err = r.prepareScaleDownShardedCluster(conn, kubeState, sc, podEnvVars, currentAgentAuthMode, log); err != nil {
		return nil, workflow.Failed("failed to perform scale down preliminary actions: %s", err)
	}

	if status := validateMongoDBResource(sc, conn); !status.IsOK() {
		return nil, status
	}

	if status := r.ensureSSLCertificates(sc, kubeState, log); !status.IsOK() {
		return nil, status
	}

	if status := controlledfeature.EnsureFeatureControls(*sc, conn, conn.OpsManagerVersion(), log); !status.IsOK() {
		return nil, status
	}

	if status := r.ensureX509InKubernetes(sc, kubeState, log); !status.IsOK() {
		return nil, status
	}

	if status := ensureRoles(sc.Spec.GetSecurity().Roles, conn, log); !status.IsOK() {
		return nil, status
	}

	status := runInGivenOrder(r.anyStatefulSetHelperNeedsToPublishState(*sc, r.client, kubeState, log),
		func() workflow.Status {
			return r.updateOmDeploymentShardedCluster(conn, sc, kubeState, podEnvVars, currentAgentAuthMode, log).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			return r.createKubernetesResources(sc, kubeState, podEnvVars, currentAgentAuthMode, log).OnErrorPrepend("Failed to create/update (Kubernetes reconciliation phase):")
		})

	if !status.IsOK() {
		return nil, status
	}
	return conn, reconcileResult

}

// anyStatefulSetHelperNeedsToPublishState checks to see if any stateful set helper part
// of the given sharded cluster needs to publish state to Ops Manager before updating Kubernetes resources
func (r *ReconcileMongoDbShardedCluster) anyStatefulSetHelperNeedsToPublishState(sc mdbv1.MongoDB, stsGetter statefulset.Getter, kubeState ShardedClusterKubeState, log *zap.SugaredLogger) bool {
	allHelpers, _ := r.getAllStatefulSetHelpers(sc, kubeState)
	for _, stsHelper := range allHelpers {
		if stsHelper.needToPublishStateFirst(stsGetter, log) {
			return true
		}
	}
	return false
}

func (r *ReconcileMongoDbShardedCluster) ensureX509InKubernetes(sc *mdbv1.MongoDB, kubeState ShardedClusterKubeState, log *zap.SugaredLogger) workflow.Status {
	security := sc.Spec.Security
	if security.Authentication != nil && !security.Authentication.Enabled {
		return workflow.OK()
	}
	useCustomCA := sc.Spec.GetTLSConfig().CA != ""

	if sc.Spec.Security.ShouldUseX509(kubeState.shardsSetsHelpers[0].CurrentAgentAuthMechanism) {
		successful, err := r.ensureX509AgentCertsForMongoDBResource(sc, useCustomCA, sc.Namespace, log)
		if err != nil {
			return workflow.Failed(err.Error())
		}
		if !successful {
			return workflow.Pending("Agent certs have not yet been approved")
		}
		if !sc.Spec.Security.TLSConfig.Enabled {
			return workflow.Failed("authentication mode for project is x509 but this MDB resource is not TLS enabled")
		} else if !r.doAgentX509CertsExist(sc.Namespace) {
			return workflow.Pending("agent x509 certificates have not yet been created")
		}
	}

	if sc.Spec.Security.GetInternalClusterAuthenticationMode() == util.X509 {
		errors := make([]error, 0)
		allSuccessful := true

		allHelpers, stsConfigs := r.getAllStatefulSetHelpers(*sc, kubeState)
		for _, helper := range allHelpers {
			if success, err := r.ensureInternalClusterCerts(*sc, stsConfigs[helper], log); err != nil {
				errors = append(errors, err)
			} else if !success {
				allSuccessful = false
			}
		}
		// fail only after creating all CSRs
		if len(errors) > 0 {
			return workflow.Failed("failed ensuring internal cluster authentication certs %s", errors[0])
		} else if !allSuccessful {
			return workflow.Pending("not all internal cluster authentication certs have been approved by Kubernetes CA")
		}
	}
	return workflow.OK()
}

func (r *ReconcileMongoDbShardedCluster) removeUnusedStatefulsets(sc *mdbv1.MongoDB, log *zap.SugaredLogger) {
	statefulsetsToRemove := sc.Status.ShardCount - sc.Spec.ShardCount
	shardsCount := sc.Status.MongodbShardedClusterSizeConfig.ShardCount

	// we iterate over last 'statefulsetsToRemove' shards if any
	for i := shardsCount - statefulsetsToRemove; i < shardsCount; i++ {
		key := objectKey(sc.Namespace, sc.ShardRsName(i))
		err := r.kubeHelper.client.DeleteStatefulSet(key)
		if err != nil {
			// Most of all the error won't be recoverable, also our sharded cluster is in good shape - we can just warn
			// the error and leave the cleanup work for the admins
			log.Warnf("Failed to delete the statefulset %s: %s", key, err)
		}
		log.Infof("Removed statefulset %s as it's was removed from sharded cluster", key)
	}
}

func (r *ReconcileMongoDbShardedCluster) ensureSSLCertificates(s *mdbv1.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger) workflow.Status {
	tlsConfig := s.Spec.GetTLSConfig()

	if tlsConfig == nil || !s.Spec.GetTLSConfig().Enabled {
		return workflow.OK()
	}

	var status workflow.Status
	status = workflow.OK()
	status = status.Merge(r.kubeHelper.ensureSSLCertsForStatefulSet(*s, certs.MongosConfig(*s, r.mongosScaler), log))
	status = status.Merge(r.kubeHelper.ensureSSLCertsForStatefulSet(*s, certs.ConfigSrvConfig(*s, r.configSrvScaler), log))

	for i := range state.shardsSetsHelpers {
		status = status.Merge(r.kubeHelper.ensureSSLCertsForStatefulSet(*s, certs.ShardConfig(*s, i, r.mongodsPerShardScaler), log))
	}

	return status
}

// createKubernetesResources creates all Kubernetes objects that are specified in 'state' parameter.
// This function returns errorStatus if any errors occured or pendingStatus if the statefulsets are not
// ready yet
// Note, that it doesn't remove any existing shards - this will be done later
func (r *ReconcileMongoDbShardedCluster) createKubernetesResources(s *mdbv1.MongoDB, state ShardedClusterKubeState, podVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) workflow.Status {

	configSrvSts, err := construct.DatabaseStatefulSet(*s, construct.ConfigServerOptions(
		Replicas(r.getConfigSrvCountThisReconciliation()),
		PodEnvVars(podVars),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.client, s.Namespace, s.ConfigRsName(), log))),
	)
	if err != nil {
		return workflow.Failed(err.Error())
	}
	err = state.configSrvSetHelper.CreateOrUpdateInKubernetes(r.client, r.client, configSrvSts)
	if err != nil {
		return workflow.Failed("Failed to create Config Server Stateful Set: %s", err)
	}
	if status := r.getStatefulSetStatus(state.configSrvSetHelper.Namespace, state.configSrvSetHelper.Name); !status.IsOK() {
		return status
	}
	_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)

	log.Infow("Created/updated StatefulSet for config servers", "name", state.configSrvSetHelper.Name, "servers count", state.configSrvSetHelper.Replicas)

	shardsNames := make([]string, s.Spec.ShardCount)

	for i, helper := range state.shardsSetsHelpers {
		shardsNames[i] = helper.Name
		shardSts, err := construct.DatabaseStatefulSet(*s, construct.ShardOptions(i,
			Replicas(r.getMongodsPerShardCountThisReconciliation()),
			PodEnvVars(podVars),
			CurrentAgentAuthMechanism(currentAgentAuthMechanism),
		),
		)

		if err != nil {
			return workflow.Failed("Failed to build Stateful Set struct for shard %s: %s", helper.Name, err)
		}

		err = helper.CreateOrUpdateInKubernetes(r.client, r.client, shardSts)
		if err != nil {
			return workflow.Failed("Failed to create Stateful Set for shard %s: %s", helper.Name, err)
		}
		if status := r.getStatefulSetStatus(helper.Namespace, helper.Name); !status.IsOK() {
			return status
		}
		_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)
	}

	log.Infow("Created/updated Stateful Sets for shards in Kubernetes", "shards", shardsNames)

	mongosSts, err := construct.DatabaseStatefulSet(*s, construct.MongosOptions(
		Replicas(r.getMongosCountThisReconciliation()),
		PodEnvVars(podVars),
		CurrentAgentAuthMechanism(currentAgentAuthMechanism),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.client, s.Namespace, s.MongosRsName(), log)),
	),
	)

	if err != nil {
		return workflow.Failed("Failed to build Stateful Set struct for mongos %s: %s", state.mongosSetHelper.Name, err)
	}

	err = state.mongosSetHelper.CreateOrUpdateInKubernetes(r.client, r.client, mongosSts)
	if err != nil {
		return workflow.Failed("Failed to create Mongos Stateful Set: %s", err)
	}

	if status := r.getStatefulSetStatus(state.mongosSetHelper.Namespace, state.mongosSetHelper.Name); !status.IsOK() {
		return status
	}
	_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)

	log.Infow("Created/updated StatefulSet for mongos servers", "name", state.mongosSetHelper.Name, "servers count", state.mongosSetHelper.Replicas)

	return workflow.OK()
}

func (r *ReconcileMongoDbShardedCluster) buildKubeObjectsForShardedCluster(s *mdbv1.MongoDB, podVars *env.PodEnvVars, projectConfig mdbv1.ProjectConfig, currentAgentAuthMechanism string, log *zap.SugaredLogger) ShardedClusterKubeState {

	mongosStartupParameters := mdbv1.StartupParameters{}
	if s.Spec.MongosSpec != nil {
		mongosStartupParameters = s.Spec.MongosSpec.Agent.StartupParameters
	}
	// 1. Create the mongos StatefulSet
	mongosBuilder := NewStatefulSetHelper(s).
		SetName(s.MongosRsName()).
		SetService(s.ServiceName()).
		SetServicePort(s.Spec.MongosSpec.GetAdditionalMongodConfig().GetPortOrDefault()).
		SetReplicas(r.getMongosCountThisReconciliation()).
		SetPodSpec(NewDefaultPodSpecWrapper(*s.Spec.MongosPodSpec)).
		SetPodVars(podVars).
		SetStartupParameters(mongosStartupParameters).
		SetLogger(log).
		SetPersistence(util.BooleanRef(false)).
		SetTLS(s.Spec.GetTLSConfig()).
		SetProjectConfig(projectConfig).
		SetSecurity(s.Spec.Security).
		SetCurrentAgentAuthMechanism(currentAgentAuthMechanism).
		SetStatefulSetConfiguration(nil) // TODO: configure once supported
	//SetStatefulSetConfiguration(s.Spec.MongosStatefulSetConfiguration)
	mongosBuilder.SetCertificateHash(enterprisepem.ReadHashFromSecret(r.client, s.Namespace, s.MongosRsName(), log))

	// 2. Create a Config Server StatefulSet
	podSpec := NewDefaultPodSpecWrapper(*s.Spec.ConfigSrvPodSpec)
	// We override the default persistence value for Config Server
	podSpec.Default.Persistence.SingleConfig.Storage = util.DefaultConfigSrvStorageSize
	configStartupParameters := mdbv1.StartupParameters{}
	if s.Spec.ConfigSrvSpec != nil {
		configStartupParameters = s.Spec.ConfigSrvSpec.Agent.StartupParameters
	}
	configBuilder := NewStatefulSetHelper(s).
		SetName(s.ConfigRsName()).
		SetService(s.ConfigSrvServiceName()).
		SetServicePort(s.Spec.ConfigSrvSpec.GetAdditionalMongodConfig().GetPortOrDefault()).
		SetReplicas(r.getConfigSrvCountThisReconciliation()).
		SetPodSpec(podSpec).
		SetPodVars(podVars).
		SetStartupParameters(configStartupParameters).
		SetLogger(log).
		SetTLS(s.Spec.GetTLSConfig()).
		SetProjectConfig(projectConfig).
		SetSecurity(s.Spec.Security).
		SetCurrentAgentAuthMechanism(currentAgentAuthMechanism).
		SetStatefulSetConfiguration(nil) // TODO: configure once supported
	//SetStatefulSetConfiguration(s.Spec.ConfigSrvStatefulSetConfiguration)
	configBuilder.SetCertificateHash(enterprisepem.ReadHashFromSecret(r.client, s.Namespace, s.ConfigRsName(), log))
	// 3. Creates a StatefulSet for each shard in the cluster
	shardStartupParameters := mdbv1.StartupParameters{}
	if s.Spec.ShardSpec != nil {
		shardStartupParameters = s.Spec.ShardSpec.Agent.StartupParameters
	}
	shardsSetHelpers := make([]*StatefulSetHelper, s.Spec.ShardCount)
	for i := 0; i < s.Spec.ShardCount; i++ {
		shardsSetHelpers[i] = NewStatefulSetHelper(s).
			SetName(s.ShardRsName(i)).
			SetService(s.ShardServiceName()).
			SetServicePort(s.Spec.ShardSpec.GetAdditionalMongodConfig().GetPortOrDefault()).
			SetReplicas(r.getMongodsPerShardCountThisReconciliation()).
			SetPodSpec(NewDefaultPodSpecWrapper(*s.Spec.ShardPodSpec)).
			SetPodVars(podVars).
			SetStartupParameters(shardStartupParameters).
			SetLogger(log).
			SetTLS(s.Spec.GetTLSConfig()).
			SetProjectConfig(projectConfig).
			SetSecurity(s.Spec.Security).
			SetCurrentAgentAuthMechanism(currentAgentAuthMechanism).
			SetStatefulSetConfiguration(nil) // TODO: configure once supported
		//SetStatefulSetConfiguration(s.Spec.ShardStatefulSetConfiguration)
		shardsSetHelpers[i].SetCertificateHash(enterprisepem.ReadHashFromSecret(r.client, s.Namespace, s.ShardRsName(i), log))
	}

	return ShardedClusterKubeState{
		mongosSetHelper:    mongosBuilder,
		configSrvSetHelper: configBuilder,
		shardsSetsHelpers:  shardsSetHelpers,
	}

}

// delete tries to complete a Deletion reconciliation event
func (r *ReconcileMongoDbShardedCluster) delete(obj interface{}, log *zap.SugaredLogger) error {
	sc := obj.(*mdbv1.MongoDB)

	projectConfig, credsConfig, err := readProjectConfigAndCredentials(r.kubeHelper.client, *sc)
	if err != nil {
		return err
	}

	conn, err := connection.PrepareOpsManagerConnection(r.kubeHelper.client, projectConfig, credsConfig, r.omConnectionFactory, sc.Namespace, log)
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
	eventHandler := MongoDBResourceEventHandler{reconciler: reconciler}
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

	zap.S().Infof("Registered controller %s", util.MongoDbShardedClusterController)

	return nil
}

func (r *ReconcileMongoDbShardedCluster) prepareScaleDownShardedCluster(omClient om.Connection, state ShardedClusterKubeState, sc *mdbv1.MongoDB, podEnvVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) error {
	membersToScaleDown := make(map[string][]string)
	clusterName := sc.Spec.GetClusterDomain()

	// Scaledown amount of replicas in ConfigServer
	if r.isConfigServerScaleDown() {
		sts, err := r.buildConfigServerStatefulSet(*sc, podEnvVars, enterprisepem.ReadHashFromSecret(r.client, sc.Namespace, sc.ConfigRsName(), log))
		if err != nil {
			return err
		}
		_, podNames := util.GetDnsForStatefulSetReplicasSpecified(sts, clusterName, sc.Status.ConfigServerCount)
		membersToScaleDown[state.configSrvSetHelper.Name] = podNames[r.getConfigSrvCountThisReconciliation():sc.Status.ConfigServerCount]
	}

	// Scaledown size of each shard
	if r.isShardsSizeScaleDown() {
		for i, s := range state.shardsSetsHelpers[:sc.Spec.ShardCount] {
			sts, err := r.buildShardStatefulSet(*sc, i, podEnvVars, currentAgentAuthMechanism)
			if err != nil {
				return err
			}
			_, podNames := util.GetDnsForStatefulSetReplicasSpecified(sts, clusterName, sc.Status.MongodsPerShardCount)
			membersToScaleDown[s.Name] = podNames[r.getMongodsPerShardCountThisReconciliation():sc.Status.MongodsPerShardCount]
		}
	}

	if len(membersToScaleDown) > 0 {
		if err := prepareScaleDown(omClient, membersToScaleDown, log); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileMongoDbShardedCluster) isConfigServerScaleDown() bool {
	return scale.ReplicasThisReconciliation(r.configSrvScaler) < r.configSrvScaler.CurrentReplicaSetMembers()
}

func (r *ReconcileMongoDbShardedCluster) isShardsSizeScaleDown() bool {
	return scale.ReplicasThisReconciliation(r.mongodsPerShardScaler) < r.mongodsPerShardScaler.CurrentReplicaSetMembers()
}

// updateOmDeploymentShardedCluster performs OM registration operation for the sharded cluster. So the changes will be finally propagated
// to automation agents in containers
// Note that the process may have two phases (if shards number is decreased):
// phase 1: "drain" the shards: remove them from sharded cluster, put replica set names to "draining" array, not remove
// replica sets and processes, wait for agents to reach the goal
// phase 2: remove the "junk" replica sets and their processes, wait for agents to reach the goal.
// The logic is designed to be idempotent: if the reconciliation is retried the controller will never skip the phase 1
// until the agents have performed draining
func (r *ReconcileMongoDbShardedCluster) updateOmDeploymentShardedCluster(conn om.Connection, sc *mdbv1.MongoDB, state ShardedClusterKubeState, podEnvVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) workflow.Status {
	err := r.waitForAgentsToRegister(sc, state, conn, podEnvVars, currentAgentAuthMechanism, log)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	dep, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err.Error())
	}

	processNames := dep.GetProcessNames(om.ShardedCluster{}, sc.Name)

	status, shardsRemoving := r.publishDeployment(conn, sc, state, podEnvVars, currentAgentAuthMechanism, log, &processNames, false)

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
		log.Infof("Some shards were removed from the sharded cluster, we need to remove them from the deployment completely")
		status, _ = r.publishDeployment(conn, sc, state, podEnvVars, currentAgentAuthMechanism, log, &processNames, true)
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

	if err = calculateDiffAndStopMonitoringHosts(conn, currentHosts, wantedHosts, log); err != nil {
		return workflow.Failed(err.Error())
	}

	if status := r.ensureBackupConfigurationAndUpdateStatus(conn, sc, log); !status.IsOK() {
		return status
	}

	log.Info("Updated Ops Manager for sharded cluster")
	return workflow.OK()
}

func (r *ReconcileMongoDbShardedCluster) publishDeployment(conn om.Connection, sc *mdbv1.MongoDB, state ShardedClusterKubeState, podEnvVars *env.PodEnvVars, currentAgentAuthMode string, log *zap.SugaredLogger,
	processNames *[]string, finalizing bool) (workflow.Status, bool) {

	// mongos
	sts, err := r.buildMongosStatefulSet(*sc, podEnvVars, enterprisepem.ReadHashFromSecret(r.client, sc.Namespace, sc.MongosRsName(), log), currentAgentAuthMode)
	if err != nil {
		return workflow.Failed(err.Error()), false
	}

	mongosProcesses := createMongosProcesses(sts, sc)

	// config server
	configSvrSts, err := r.buildConfigServerStatefulSet(*sc, podEnvVars, enterprisepem.ReadHashFromSecret(r.client, sc.Namespace, sc.ConfigRsName(), log))
	if err != nil {
		return workflow.Failed(err.Error()), false
	}
	configRs := buildReplicaSetFromProcesses(configSvrSts.Name, createConfigSrvProcesses(configSvrSts, sc), sc)

	// shards
	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i := range state.shardsSetsHelpers {
		shardSts, err := r.buildShardStatefulSet(*sc, i, podEnvVars, currentAgentAuthMode)
		if err != nil {
			return workflow.Failed(err.Error()), false
		}
		shards[i] = buildReplicaSetFromProcesses(shardSts.Name, createShardProcesses(shardSts, sc), sc)
	}

	status, additionalReconciliationRequired := r.updateOmAuthentication(conn, *processNames, sc, log)
	if !status.IsOK() {
		return status, false
	}

	shardsRemoving := false
	err = conn.ReadUpdateDeployment(
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
			if shardsRemoving, err = d.MergeShardedCluster(sc.Name, mongosProcesses, configRs, shards, finalizing); err != nil {
				return err
			}
			d.AddMonitoringAndBackup(log, sc.Spec.GetTLSConfig().IsEnabled())
			d.ConfigureTLS(sc.Spec.GetTLSConfig())

			*processNames = d.GetProcessNames(om.ShardedCluster{}, sc.Name)
			d.ConfigureInternalClusterAuthentication(*processNames, sc.Spec.Security.GetInternalClusterAuthenticationMode())

			return nil
		},
		log,
	)

	if err != nil {
		return workflow.Failed(err.Error()), shardsRemoving
	}

	if err := om.WaitForReadyState(conn, *processNames, log); err != nil {
		return workflow.Failed(err.Error()), shardsRemoving
	}

	if additionalReconciliationRequired {
		return workflow.Pending("Performing multi stage reconciliation"), shardsRemoving
	}

	return workflow.OK(), shardsRemoving
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

func (r *ReconcileMongoDbShardedCluster) waitForAgentsToRegister(cluster *mdbv1.MongoDB, state ShardedClusterKubeState, conn om.Connection, podEnvVars *env.PodEnvVars, currentAgentAuthMode string,
	log *zap.SugaredLogger) error {
	mongosStatefulSet, err := r.buildMongosStatefulSet(*cluster, podEnvVars, enterprisepem.ReadHashFromSecret(r.client, cluster.Namespace, cluster.MongosRsName(), log), currentAgentAuthMode)
	if err != nil {
		return err
	}

	if err := waitForRsAgentsToRegister(mongosStatefulSet, cluster.Spec.GetClusterDomain(), conn, log); err != nil {
		return err
	}

	configSrvStatefulSet, err := r.buildConfigServerStatefulSet(*cluster, podEnvVars, enterprisepem.ReadHashFromSecret(r.client, cluster.Namespace, cluster.ConfigRsName(), log))
	if err != nil {
		return err
	}

	if err := waitForRsAgentsToRegister(configSrvStatefulSet, cluster.Spec.GetClusterDomain(), conn, log); err != nil {
		return err
	}

	for i := range state.shardsSetsHelpers {
		shardStatefulSet, err := r.buildShardStatefulSet(*cluster, i, podEnvVars, currentAgentAuthMode)
		if err != nil {
			return err
		}

		if err := waitForRsAgentsToRegister(shardStatefulSet, cluster.Spec.GetClusterDomain(), conn, log); err != nil {
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

	hosts, _ := util.GetDNSNames(c.MongosRsName(), c.ServiceName(), c.Namespace, c.Spec.GetClusterDomain(), sizeConfig.MongosCount)
	ans = append(ans, hosts...)

	hosts, _ = util.GetDNSNames(c.ConfigRsName(), c.ConfigSrvServiceName(), c.Namespace, c.Spec.GetClusterDomain(), sizeConfig.ConfigServerCount)
	ans = append(ans, hosts...)

	for i := 0; i < sizeConfig.ShardCount; i++ {
		hosts, _ = util.GetDNSNames(c.ShardRsName(i), c.ShardServiceName(), c.Namespace, c.Spec.GetClusterDomain(), sizeConfig.MongodsPerShardCount)
		ans = append(ans, hosts...)
	}
	return ans
}

// getAllStatefulSetHelpers returns a list of all StatefulSetHelpers that
// make up a Sharded Cluster
func (r *ReconcileMongoDbShardedCluster) getAllStatefulSetHelpers(sc mdbv1.MongoDB, kubeState ShardedClusterKubeState) ([]*StatefulSetHelper, map[*StatefulSetHelper]certs.Options) {
	stsHelpers := make([]*StatefulSetHelper, 0)
	stsToCertsMap := make(map[*StatefulSetHelper]certs.Options)

	for i, stsHelper := range kubeState.shardsSetsHelpers {
		stsHelpers = append(stsHelpers, stsHelper)
		stsToCertsMap[stsHelper] = certs.ShardConfig(sc, i, r.mongodsPerShardScaler)
	}

	stsHelpers = append(stsHelpers, kubeState.mongosSetHelper)
	stsToCertsMap[kubeState.mongosSetHelper] = certs.MongosConfig(sc, r.mongosScaler)

	stsHelpers = append(stsHelpers, kubeState.configSrvSetHelper)
	stsToCertsMap[kubeState.configSrvSetHelper] = certs.ConfigSrvConfig(sc, r.configSrvScaler)

	return stsHelpers, stsToCertsMap
}

func createMongosProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB) []om.Process {
	hostnames, names := util.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain())
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongosProcess(names[idx], hostname, mdb)
	}

	return processes
}
func createConfigSrvProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB) []om.Process {
	var configSrvAdditionalConfig mdbv1.AdditionalMongodConfig
	if mdb.Spec.ConfigSrvSpec != nil {
		configSrvAdditionalConfig = mdb.Spec.ConfigSrvSpec.AdditionalMongodConfig
	}

	return createMongodProcessForShardedCluster(set, configSrvAdditionalConfig, mdb)
}
func createShardProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB) []om.Process {
	var shardAdditionalConfig mdbv1.AdditionalMongodConfig
	if mdb.Spec.ShardSpec != nil {
		shardAdditionalConfig = mdb.Spec.ShardSpec.AdditionalMongodConfig
	}

	return createMongodProcessForShardedCluster(set, shardAdditionalConfig, mdb)
}
func createMongodProcessForShardedCluster(set appsv1.StatefulSet, additionalMongodConfig mdbv1.AdditionalMongodConfig, mdb *mdbv1.MongoDB) []om.Process {
	hostnames, names := util.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain())
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := calculateWiredTigerCache(set, mdb.Spec.GetVersion())

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(names[idx], hostname, additionalMongodConfig, mdb)
		if wiredTigerCache != nil {
			processes[idx].SetWiredTigerCache(*wiredTigerCache)
		}
	}

	return processes
}

// buildReplicaSetFromProcesses creates the 'ReplicaSetWithProcesses' with specified processes. This is of use only
// for sharded cluster (config server, shards)
func buildReplicaSetFromProcesses(name string, members []om.Process, mdb *mdbv1.MongoDB) om.ReplicaSetWithProcesses {
	replicaSet := om.NewReplicaSet(name, mdb.Spec.GetVersion())
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

func (r shardedClusterScaler) DesiredReplicaSetMembers() int {
	return r.DesiredMembers
}

func (r shardedClusterScaler) CurrentReplicaSetMembers() int {
	return r.CurrentMembers
}

func (r *ReconcileMongoDbShardedCluster) buildConfigServerStatefulSet(sc mdbv1.MongoDB, podVars *env.PodEnvVars, pemHash string) (appsv1.StatefulSet, error) {
	return construct.DatabaseStatefulSet(sc, construct.ConfigServerOptions(
		Replicas(r.getConfigSrvCountThisReconciliation()),
		PodEnvVars(podVars),
		CertificateHash(pemHash),
	),
	)
}

func (r *ReconcileMongoDbShardedCluster) buildMongosStatefulSet(sc mdbv1.MongoDB, podVars *env.PodEnvVars, pemHash, currentAgentAuthMechanism string) (appsv1.StatefulSet, error) {
	return construct.DatabaseStatefulSet(sc, construct.MongosOptions(
		Replicas(r.getMongosCountThisReconciliation()),
		PodEnvVars(podVars),
		CurrentAgentAuthMechanism(currentAgentAuthMechanism),
		CertificateHash(pemHash),
	),
	)
}

func (r *ReconcileMongoDbShardedCluster) buildShardStatefulSet(sc mdbv1.MongoDB, shardNum int, podVars *env.PodEnvVars, currentAgentAuthMechanism string) (appsv1.StatefulSet, error) {
	return construct.DatabaseStatefulSet(sc, construct.ShardOptions(shardNum,
		Replicas(r.getMongodsPerShardCountThisReconciliation()),
		PodEnvVars(podVars),
		CurrentAgentAuthMechanism(currentAgentAuthMechanism),
	),
	)
}
