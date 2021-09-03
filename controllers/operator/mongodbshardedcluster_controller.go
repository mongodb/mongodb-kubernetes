package operator

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/replicaset"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/wiredtiger"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"

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

func (r *ReconcileMongoDbShardedCluster) Reconcile(_ context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	agents.UpgradeAllIfNeeded(r.client, r.omConnectionFactory, getWatchedNamespace())

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

	conn, status := r.doShardedClusterProcessing(sc, log)
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
func (r *ReconcileMongoDbShardedCluster) doShardedClusterProcessing(obj interface{}, log *zap.SugaredLogger) (om.Connection, workflow.Status) {
	log.Info("ShardedCluster.doShardedClusterProcessing")
	sc := obj.(*mdbv1.MongoDB)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, sc, log)
	if err != nil {
		return nil, workflow.Failed(err.Error())
	}

	conn, err := connection.PrepareOpsManagerConnection(r.client, projectConfig, credsConfig, r.omConnectionFactory, sc.Namespace, log)
	if err != nil {
		return nil, workflow.Failed(err.Error())
	}

	if status := ensureSupportedOpsManagerVersion(conn); status.Phase() != mdbstatus.PhaseRunning {
		return nil, status
	}
	r.RegisterWatchedMongodbResources(sc.ObjectKey(), sc.Spec.GetProject(), sc.Spec.Credentials)

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

	if err = r.prepareScaleDownShardedCluster(conn, sc, podEnvVars, currentAgentAuthMode, log); err != nil {
		return nil, workflow.Failed("failed to perform scale down preliminary actions: %s", err)
	}

	if status := validateMongoDBResource(sc, conn); !status.IsOK() {
		return nil, status
	}

	if status := r.ensureSSLCertificates(sc, log); !status.IsOK() {
		return nil, status
	}

	if status := controlledfeature.EnsureFeatureControls(*sc, conn, conn.OpsManagerVersion(), log); !status.IsOK() {
		return nil, status
	}

	if status := r.ensureX509InKubernetes(sc, currentAgentAuthMode, log); !status.IsOK() {
		return nil, status
	}

	if status := ensureRoles(sc.Spec.GetSecurity().Roles, conn, log); !status.IsOK() {
		return nil, status
	}

	allConfigs := r.getAllConfigs(*sc, podEnvVars, currentAgentAuthMode, log)
	status := workflow.RunInGivenOrder(anyStatefulSetNeedsToPublishState(*sc, r.client, allConfigs, log),
		func() workflow.Status {
			return r.updateOmDeploymentShardedCluster(conn, sc, podEnvVars, currentAgentAuthMode, log).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			return r.createKubernetesResources(sc, podEnvVars, currentAgentAuthMode, log).OnErrorPrepend("Failed to create/update (Kubernetes reconciliation phase):")
		})

	if !status.IsOK() {
		return nil, status
	}
	return conn, reconcileResult
}

// anyStatefulSetNeedsToPublishState checks to see if any stateful set
// of the given sharded cluster needs to publish state to Ops Manager before updating Kubernetes resources
func anyStatefulSetNeedsToPublishState(sc mdbv1.MongoDB, stsGetter statefulset.Getter, configs []func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) bool {
	for _, cf := range configs {
		if needToPublishStateFirst(stsGetter, sc, cf, log) {
			return true
		}
	}
	return false
}

// getAllConfigs returns a list of all the configuration functions associated with the Sharded Cluster.
// This includes the Mongos, the Config Server and all Shards
func (r ReconcileMongoDbShardedCluster) getAllConfigs(sc mdbv1.MongoDB, podVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) []func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	allConfigs := make([]func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, 0)
	for i := 0; i < sc.Spec.ShardCount; i++ {
		allConfigs = append(allConfigs, r.getShardOptions(sc, i, podVars, currentAgentAuthMechanism, log))
	}
	allConfigs = append(allConfigs, r.getConfigServerOptions(sc, podVars, currentAgentAuthMechanism, log))
	allConfigs = append(allConfigs, r.getMongosOptions(sc, podVars, currentAgentAuthMechanism, log))
	return allConfigs
}

func (r *ReconcileMongoDbShardedCluster) ensureX509InKubernetes(sc *mdbv1.MongoDB, currentAgentAuthMechanism string, log *zap.SugaredLogger) workflow.Status {
	security := sc.Spec.Security
	if security.Authentication != nil && !security.Authentication.Enabled {
		return workflow.OK()
	}

	if sc.Spec.Security.ShouldUseX509(currentAgentAuthMechanism) {
		if err := certs.VerifyClientCertificatesForAgents(r.client, sc.Namespace); err != nil {
			return workflow.Failed(err.Error())
		}

		if !sc.Spec.Security.TLSConfig.Enabled {
			return workflow.Failed("authentication mode for project is x509 but this MDB resource is not TLS enabled")
		}
	}

	if sc.Spec.Security.GetInternalClusterAuthenticationMode() == util.X509 {
		errors := make([]error, 0)
		allCertOptions := r.getAllCertOptions(*sc)
		for _, certOption := range allCertOptions {
			if err := r.ensureInternalClusterCerts(*sc, certOption, log); err != nil {
				errors = append(errors, err)
			}
		}
		if len(errors) > 0 {
			return workflow.Failed("failed ensuring internal cluster authentication certs %s", errors[0])
		}
	}
	return workflow.OK()
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

func (r *ReconcileMongoDbShardedCluster) ensureSSLCertificates(s *mdbv1.MongoDB, log *zap.SugaredLogger) workflow.Status {
	tlsConfig := s.Spec.GetTLSConfig()

	if tlsConfig == nil || !s.Spec.GetTLSConfig().IsEnabled() {
		return workflow.OK()
	}

	var status workflow.Status
	status = workflow.OK()
	status = status.Merge(certs.EnsureSSLCertsForStatefulSet(r.client, *s.Spec.Security, certs.MongosConfig(*s, r.mongosScaler), log))
	status = status.Merge(certs.EnsureSSLCertsForStatefulSet(r.client, *s.Spec.Security, certs.ConfigSrvConfig(*s, r.configSrvScaler), log))

	for i := 0; i < s.Spec.ShardCount; i++ {
		status = status.Merge(certs.EnsureSSLCertsForStatefulSet(r.client, *s.Spec.Security, certs.ShardConfig(*s, i, r.mongodsPerShardScaler), log))
	}

	return status
}

// createKubernetesResources creates all Kubernetes objects that are specified in 'state' parameter.
// This function returns errorStatus if any errors occured or pendingStatus if the statefulsets are not
// ready yet
// Note, that it doesn't remove any existing shards - this will be done later
func (r *ReconcileMongoDbShardedCluster) createKubernetesResources(s *mdbv1.MongoDB, podVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) workflow.Status {
	configSrvOpts := r.getConfigServerOptions(*s, podVars, currentAgentAuthMechanism, log)
	configSrvSts := construct.DatabaseStatefulSet(*s, configSrvOpts)
	if err := create.DatabaseInKubernetes(r.client, *s, configSrvSts, configSrvOpts, log); err != nil {
		return workflow.Failed("Failed to create Config Server Stateful Set: %s", err)
	}
	if status := r.getStatefulSetStatus(s.Namespace, s.ConfigRsName()); !status.IsOK() {
		return status
	}
	_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)

	log.Infow("Created/updated StatefulSet for config servers", "name", s.ConfigRsName(), "servers count", configSrvSts.Spec.Replicas)

	shardsNames := make([]string, s.Spec.ShardCount)

	for i := 0; i < s.Spec.ShardCount; i++ {
		shardsNames[i] = s.ShardRsName(i)
		shardOpts := r.getShardOptions(*s, i, podVars, currentAgentAuthMechanism, log)
		shardSts := construct.DatabaseStatefulSet(*s, shardOpts)

		if err := create.DatabaseInKubernetes(r.client, *s, shardSts, shardOpts, log); err != nil {
			return workflow.Failed("Failed to create Stateful Set for shard %s: %s", shardsNames[i], err)
		}
		if status := r.getStatefulSetStatus(s.Namespace, shardsNames[i]); !status.IsOK() {
			return status
		}
		_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)
	}

	log.Infow("Created/updated Stateful Sets for shards in Kubernetes", "shards", shardsNames)

	mongosOpts := r.getMongosOptions(*s, podVars, currentAgentAuthMechanism, log)
	mongosSts := construct.DatabaseStatefulSet(*s, mongosOpts)

	if err := create.DatabaseInKubernetes(r.client, *s, mongosSts, mongosOpts, log); err != nil {
		return workflow.Failed("Failed to create Mongos Stateful Set: %s", err)
	}

	if status := r.getStatefulSetStatus(s.Namespace, s.MongosRsName()); !status.IsOK() {
		return status
	}
	_, _ = r.updateStatus(s, workflow.Reconciling().WithResourcesNotReady([]mdbstatus.ResourceNotReady{}).WithNoMessage(), log)

	log.Infow("Created/updated StatefulSet for mongos servers", "name", s.MongosRsName(), "servers count", mongosSts.Spec.Replicas)
	return workflow.OK()
}

// OnDelete tries to complete a Deletion reconciliation event
func (r *ReconcileMongoDbShardedCluster) OnDelete(obj runtime.Object, log *zap.SugaredLogger) error {
	sc := obj.(*mdbv1.MongoDB)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, sc, log)
	if err != nil {
		return err
	}

	conn, err := connection.PrepareOpsManagerConnection(r.client, projectConfig, credsConfig, r.omConnectionFactory, sc.Namespace, log)
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

	r.RemoveMongodbWatchedResources(sc.ObjectKey())

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

	zap.S().Infof("Registered controller %s", util.MongoDbShardedClusterController)

	return nil
}

func (r *ReconcileMongoDbShardedCluster) prepareScaleDownShardedCluster(omClient om.Connection, sc *mdbv1.MongoDB, podEnvVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) error {
	membersToScaleDown := make(map[string][]string)
	clusterName := sc.Spec.GetClusterDomain()

	// Scaledown amount of replicas in ConfigServer
	if r.isConfigServerScaleDown() {
		sts := construct.DatabaseStatefulSet(*sc, r.getConfigServerOptions(*sc, podEnvVars, currentAgentAuthMechanism, log))
		_, podNames := dns.GetDnsForStatefulSetReplicasSpecified(sts, clusterName, sc.Status.ConfigServerCount)
		membersToScaleDown[sc.ConfigRsName()] = podNames[r.getConfigSrvCountThisReconciliation():sc.Status.ConfigServerCount]
	}

	// Scaledown size of each shard
	if r.isShardsSizeScaleDown() {
		for i := 0; i < sc.Spec.ShardCount; i++ {
			sts := construct.DatabaseStatefulSet(*sc, r.getShardOptions(*sc, i, podEnvVars, currentAgentAuthMechanism, log))
			_, podNames := dns.GetDnsForStatefulSetReplicasSpecified(sts, clusterName, sc.Status.MongodsPerShardCount)
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

// updateOmDeploymentShardedCluster performs OM registration operation for the sharded cluster. So the changes will be finally propagated
// to automation agents in containers
// Note that the process may have two phases (if shards number is decreased):
// phase 1: "drain" the shards: remove them from sharded cluster, put replica set names to "draining" array, not remove
// replica sets and processes, wait for agents to reach the goal
// phase 2: remove the "junk" replica sets and their processes, wait for agents to reach the goal.
// The logic is designed to be idempotent: if the reconciliation is retried the controller will never skip the phase 1
// until the agents have performed draining
func (r *ReconcileMongoDbShardedCluster) updateOmDeploymentShardedCluster(conn om.Connection, sc *mdbv1.MongoDB, podEnvVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) workflow.Status {
	err := r.waitForAgentsToRegister(sc, conn, podEnvVars, currentAgentAuthMechanism, log)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	dep, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err.Error())
	}

	processNames := dep.GetProcessNames(om.ShardedCluster{}, sc.Name)

	status, shardsRemoving := r.publishDeployment(conn, sc, podEnvVars, currentAgentAuthMechanism, log, &processNames, false)

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
		status, _ = r.publishDeployment(conn, sc, podEnvVars, currentAgentAuthMechanism, log, &processNames, true)
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

	if status := r.ensureBackupConfigurationAndUpdateStatus(conn, sc, log); !status.IsOK() {
		return status
	}

	log.Info("Updated Ops Manager for sharded cluster")
	return workflow.OK()
}

func (r *ReconcileMongoDbShardedCluster) publishDeployment(conn om.Connection, sc *mdbv1.MongoDB, podEnvVars *env.PodEnvVars, currentAgentAuthMode string, log *zap.SugaredLogger,
	processNames *[]string, finalizing bool) (workflow.Status, bool) {

	// mongos
	sts := construct.DatabaseStatefulSet(*sc, r.getMongosOptions(*sc, podEnvVars, currentAgentAuthMode, log))
	mongosProcesses := createMongosProcesses(sts, sc)

	// config server
	configSvrSts := construct.DatabaseStatefulSet(*sc, r.getConfigServerOptions(*sc, podEnvVars, currentAgentAuthMode, log))
	configRs := buildReplicaSetFromProcesses(configSvrSts.Name, createConfigSrvProcesses(configSvrSts, sc), sc)

	// shards
	shards := make([]om.ReplicaSetWithProcesses, sc.Spec.ShardCount)
	for i := 0; i < sc.Spec.ShardCount; i++ {
		shardSts := construct.DatabaseStatefulSet(*sc, r.getShardOptions(*sc, i, podEnvVars, currentAgentAuthMode, log))
		shards[i] = buildReplicaSetFromProcesses(shardSts.Name, createShardProcesses(shardSts, sc), sc)
	}

	status, additionalReconciliationRequired := r.updateOmAuthentication(conn, *processNames, sc, log)
	if !status.IsOK() {
		return status, false
	}

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
			var err error
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

func (r *ReconcileMongoDbShardedCluster) waitForAgentsToRegister(sc *mdbv1.MongoDB, conn om.Connection, podEnvVars *env.PodEnvVars, currentAgentAuthMode string,
	log *zap.SugaredLogger) error {
	mongosStatefulSet := construct.DatabaseStatefulSet(*sc, r.getMongosOptions(*sc, podEnvVars, currentAgentAuthMode, log))
	if err := agents.WaitForRsAgentsToRegister(mongosStatefulSet, sc.Spec.GetClusterDomain(), conn, log); err != nil {
		return err
	}

	configSrvStatefulSet := construct.DatabaseStatefulSet(*sc, r.getConfigServerOptions(*sc, podEnvVars, currentAgentAuthMode, log))
	if err := agents.WaitForRsAgentsToRegister(configSrvStatefulSet, sc.Spec.GetClusterDomain(), conn, log); err != nil {
		return err
	}

	for i := 0; i < sc.Spec.ShardCount; i++ {
		shardStatefulSet := construct.DatabaseStatefulSet(*sc, r.getShardOptions(*sc, i, podEnvVars, currentAgentAuthMode, log))
		if err := agents.WaitForRsAgentsToRegister(shardStatefulSet, sc.Spec.GetClusterDomain(), conn, log); err != nil {
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

	hosts, _ := dns.GetDNSNames(c.MongosRsName(), c.ServiceName(), c.Namespace, c.Spec.GetClusterDomain(), sizeConfig.MongosCount)
	ans = append(ans, hosts...)

	hosts, _ = dns.GetDNSNames(c.ConfigRsName(), c.ConfigSrvServiceName(), c.Namespace, c.Spec.GetClusterDomain(), sizeConfig.ConfigServerCount)
	ans = append(ans, hosts...)

	for i := 0; i < sizeConfig.ShardCount; i++ {
		hosts, _ = dns.GetDNSNames(c.ShardRsName(i), c.ShardServiceName(), c.Namespace, c.Spec.GetClusterDomain(), sizeConfig.MongodsPerShardCount)
		ans = append(ans, hosts...)
	}
	return ans
}

func (r *ReconcileMongoDbShardedCluster) getAllCertOptions(sc mdbv1.MongoDB) []certs.Options {
	certOptions := make([]certs.Options, 0)

	for i := 0; i < sc.Spec.ShardCount; i++ {
		certOptions = append(certOptions, certs.ShardConfig(sc, i, r.mongodsPerShardScaler))
	}
	certOptions = append(certOptions, certs.MongosConfig(sc, r.mongosScaler))
	certOptions = append(certOptions, certs.ConfigSrvConfig(sc, r.configSrvScaler))
	return certOptions
}

func createMongosProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB) []om.Process {
	hostnames, names := dns.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain())
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongosProcess(names[idx], hostname, mdb.Spec.MongosSpec.GetAdditionalMongodConfig(), mdb.GetSpec())
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
	hostnames, names := dns.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain())
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := wiredtiger.CalculateCache(set, util.DatabaseContainerName, mdb.Spec.GetMongoDBVersion())

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(names[idx], hostname, additionalMongodConfig, &mdb.Spec)
		if wiredTigerCache != nil {
			processes[idx].SetWiredTigerCache(*wiredTigerCache)
		}
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

func (r shardedClusterScaler) DesiredReplicas() int {
	return r.DesiredMembers
}

func (r shardedClusterScaler) CurrentReplicas() int {
	return r.CurrentMembers
}

// getConfigServerOptions returns the Options needed to build the StatefulSet for the config server.
func (r *ReconcileMongoDbShardedCluster) getConfigServerOptions(sc mdbv1.MongoDB, podVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	certSecretName := certs.ConfigSrvConfig(sc, r.configSrvScaler).CertSecretName
	return construct.ConfigServerOptions(
		Replicas(r.getConfigSrvCountThisReconciliation()),
		PodEnvVars(podVars),
		CurrentAgentAuthMechanism(currentAgentAuthMechanism),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.client, sc.Namespace, certSecretName, log)),
	)
}

// getMongosOptions returns the Options needed to build the StatefulSet for the mongos.
func (r *ReconcileMongoDbShardedCluster) getMongosOptions(sc mdbv1.MongoDB, podVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	certSecretName := certs.MongosConfig(sc, r.mongosScaler).CertSecretName
	return construct.MongosOptions(
		Replicas(r.getMongosCountThisReconciliation()),
		PodEnvVars(podVars),
		CurrentAgentAuthMechanism(currentAgentAuthMechanism),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.client, sc.Namespace, certSecretName, log)))
}

// getShardOptions returns the Options needed to build the StatefulSet for a given shard.
func (r *ReconcileMongoDbShardedCluster) getShardOptions(sc mdbv1.MongoDB, shardNum int, podVars *env.PodEnvVars, currentAgentAuthMechanism string, log *zap.SugaredLogger) func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	certSecretName := certs.ShardConfig(sc, shardNum, r.mongodsPerShardScaler).CertSecretName
	return construct.ShardOptions(shardNum,
		Replicas(r.getMongodsPerShardCountThisReconciliation()),
		PodEnvVars(podVars),
		CurrentAgentAuthMechanism(currentAgentAuthMechanism),
		CertificateHash(enterprisepem.ReadHashFromSecret(r.client, sc.Namespace, certSecretName, log)))
}
