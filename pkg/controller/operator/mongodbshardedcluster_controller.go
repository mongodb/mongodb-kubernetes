package operator

import (
	"fmt"

	"go.uber.org/zap"
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

// ReconcileMongoDbShardedCluster
type ReconcileMongoDbShardedCluster struct {
	*ReconcileCommonController
}

func newShardedClusterReconciler(mgr manager.Manager, omFunc om.ConnectionFunc) *ReconcileMongoDbShardedCluster {
	return &ReconcileMongoDbShardedCluster{newReconcileCommonController(mgr, omFunc)}
}

// Reconcile
func (r *ReconcileMongoDbShardedCluster) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("ShardedCluster", request.NamespacedName)
	sc := &mongodb.MongoDbShardedCluster{}

	defer exceptionHandling(
		func() (reconcile.Result, error) {
			return r.updateStatusFailed(sc, "Failed to reconcile Sharded Cluster", log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	reconcileResult, err := r.prepareResourceForReconciliation(request, sc, log)
	if reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Info("-> ShardedCluster.Reconcile")
	log.Infow("ShardedCluster.Spec", "spec", sc.Spec)
	log.Infow("ShardedCluster.Status", "status", sc.Status)

	conn, err := r.doShardedClusterProcessing(sc, log)
	if err != nil {
		return r.updateStatusFailed(sc, err.Error(), log)
	}

	r.updateStatusSuccessful(sc, log, conn.BaseURL(), conn.GroupID())
	log.Infof("Finished reconciliation for Sharded Cluster! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return reconcile.Result{}, nil
}

// implements all the logic to do the sharded cluster thing
func (r *ReconcileMongoDbShardedCluster) doShardedClusterProcessing(obj interface{}, log *zap.SugaredLogger) (om.Connection, error) {
	log.Info("ShardedCluster.doShardedClusterProcessing")
	sc := obj.(*mongodb.MongoDbShardedCluster)
	podVars := &PodVars{}
	conn, err := r.prepareConnection(objectKey(sc.Namespace, sc.Name), sc.Spec.CommonSpec, podVars, log)
	if err != nil {
		return nil, err
	}

	kubeState := r.buildKubeObjectsForShardedCluster(sc, podVars, log)

	if err = prepareScaleDownShardedCluster(conn, kubeState, sc, log); err != nil {
		return nil, fmt.Errorf("Failed to perform scale down preliminary actions: %s", err)
	}

	if err = r.createKubernetesResources(sc, kubeState, log); err != nil {
		return nil, fmt.Errorf("Failed to create/update resources in Kubernetes for sharded cluster: %s", err)
	}
	log.Infow("All Kubernetes objects are created/updated, adding the deployment to Ops Manager")

	if err := updateOmDeploymentShardedCluster(conn, sc, kubeState, log); err != nil {
		return nil, fmt.Errorf("Failed to update OpsManager automation config: %s", err)
	}
	log.Infow("Ops Manager deployment updated successfully")

	return conn, nil

}

func (r *ReconcileMongoDbShardedCluster) createKubernetesResources(s *mongodb.MongoDbShardedCluster, state ShardedClusterKubeState, log *zap.SugaredLogger) error {
	err := state.mongosSetHelper.CreateOrUpdateInKubernetes()
	if err != nil {
		return fmt.Errorf("Failed to create Mongos Stateful Set: %s", err)
	}

	log.Infow("Created StatefulSet for mongos servers", "name", state.mongosSetHelper.Name, "servers count", state.mongosSetHelper.Replicas)

	err = state.configSrvSetHelper.CreateOrUpdateInKubernetes()
	if err != nil {
		return fmt.Errorf("Failed to create Config Server Stateful Set: %s", err)
	}

	log.Infow("Created StatefulSet for config servers", "name", state.configSrvSetHelper.Name, "servers count", state.configSrvSetHelper.Replicas)

	shardsNames := make([]string, s.Spec.ShardCount)

	for i, s := range state.shardsSetsHelpers {
		shardsNames[i] = s.Name
		err = s.CreateOrUpdateInKubernetes()
		if err != nil {
			return fmt.Errorf("Failed to create Stateful Set for shard %s: %s", s.Name, err)
		}
	}
	log.Infow("Created Stateful Sets for shards in Kubernetes", "shards", shardsNames)

	return nil
}

func (r *ReconcileMongoDbShardedCluster) buildKubeObjectsForShardedCluster(s *mongodb.MongoDbShardedCluster, podVars *PodVars, log *zap.SugaredLogger) ShardedClusterKubeState {
	// 1. Create the mongos StatefulSet
	mongosBuilder := r.kubeHelper.NewStatefulSetHelper(s).
		SetName(s.MongosRsName()).
		SetService(s.MongosServiceName()).
		SetReplicas(s.Spec.MongosCount).
		SetPodSpec(NewDefaultPodSpecWrapper(s.Spec.MongosPodSpec)).
		SetPodVars(podVars).
		SetLogger(log).
		SetPersistence(util.BooleanRef(false)).
		SetExposedExternally(true)

	// 2. Create a Config Server StatefulSet
	defaultConfigSrvSpec := NewDefaultPodSpec()
	defaultConfigSrvSpec.Persistence.SingleConfig.Storage = util.DefaultConfigSrvStorageSize
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: s.Spec.ConfigSrvPodSpec,
		Default:        defaultConfigSrvSpec,
	}
	configBuilder := r.kubeHelper.NewStatefulSetHelper(s).
		SetName(s.ConfigRsName()).
		SetService(s.ConfigSrvServiceName()).
		SetReplicas(s.Spec.ConfigServerCount).
		SetPersistence(s.Spec.Persistent).
		SetPodSpec(podSpec).
		SetPodVars(podVars).
		SetLogger(log).
		SetExposedExternally(false)

	// 3. Creates a StatefulSet for each shard in the cluster
	shardsSetHelpers := make([]*StatefulSetHelper, s.Spec.ShardCount)
	for i := 0; i < s.Spec.ShardCount; i++ {
		shardsSetHelpers[i] = r.kubeHelper.NewStatefulSetHelper(s).
			SetName(s.ShardRsName(i)).
			SetService(s.ShardServiceName()).
			SetReplicas(s.Spec.MongodsPerShardCount).
			SetPersistence(s.Spec.Persistent).
			SetPodSpec(NewDefaultPodSpecWrapper(s.Spec.ShardPodSpec)).
			SetPodVars(podVars).
			SetLogger(log)
	}

	return ShardedClusterKubeState{
		mongosSetHelper:    mongosBuilder,
		configSrvSetHelper: configBuilder,
		shardsSetsHelpers:  shardsSetHelpers,
	}

}

// delete tries to complete a Deletion reconciliation event
func (r *ReconcileMongoDbShardedCluster) delete(obj interface{}, log *zap.SugaredLogger) error {
	// TODO: find a standard & consistent way of logging these events
	sc := obj.(*mongodb.MongoDbShardedCluster)

	conn, err := r.prepareConnection(objectKey(sc.Namespace, sc.Name), sc.Spec.CommonSpec, nil, log)
	if err != nil {
		return err
	}
	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			processNames = d.GetProcessNames(om.ShardedCluster{}, sc.Name)
			if e := d.RemoveShardedClusterByName(sc.Name); e != nil {
				log.Warnf("Failed to remove sharded cluster from automation config: %s", e)
			}
			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	if err := conn.WaitForReadyState(processNames, log); err != nil {
		return err
	}

	err = om.StopBackupIfEnabled(conn, sc.Name, om.ShardedClusterType, log)
	if err != nil {
		return err
	}

	sizeConfig := getMaxShardedClusterSizeConfig(sc.Spec.MongodbShardedClusterSizeConfig, sc.Status.MongodbShardedClusterSizeConfig)
	hostsToRemove := getAllHosts(sc, sizeConfig)
	log.Infow("Stop monitoring removed hosts in Ops Manager", "hostsToBeRemoved", hostsToRemove)

	if err = om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	log.Info("Removed sharded cluster from Ops Manager!")

	return nil
}

// AddShardedClusterController
func AddShardedClusterController(mgr manager.Manager) error {
	reconciler := newShardedClusterReconciler(mgr, om.NewOpsManagerConnection)
	options := controller.Options{Reconciler: reconciler}
	c, err := controller.New(util.MongoDbShardedClusterController, mgr, options)
	if err != nil {
		return err
	}

	// watch for changes to sharded cluster MongoDB resources
	eventHandler := MongoDBResourceEventHandler{reconciler: reconciler}
	err = c.Watch(&source.Kind{Type: &mongodb.MongoDbShardedCluster{}}, &eventHandler, predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mongodb.MongoDbShardedCluster)
			newResource := e.ObjectNew.(*mongodb.MongoDbShardedCluster)
			return shouldReconcile(oldResource, newResource)
		}})
	if err != nil {
		return err
	}

	// TODO CLOUDP-35240
	/*err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &mongodb.MongoDbShardedCluster{},
	})
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

	zap.S().Infof("Registered controller %s", util.MongoDbShardedClusterController)

	return nil
}

func prepareScaleDownShardedCluster(omClient om.Connection, state ShardedClusterKubeState, sc *mongodb.MongoDbShardedCluster, log *zap.SugaredLogger) error {
	membersToScaleDown := make(map[string][]string)
	clusterName := sc.Spec.ClusterName

	// Scaledown amount of replicas in ConfigServer
	if isConfigServerScaleDown(sc) {
		_, podNames := GetDnsForStatefulSetReplicasSpecified(state.configSrvSetHelper.BuildStatefulSet(), clusterName, sc.Status.ConfigServerCount)
		membersToScaleDown[state.configSrvSetHelper.Name] = podNames[sc.Spec.ConfigServerCount:sc.Status.ConfigServerCount]
	}

	// Scaledown size of each shard
	if isShardsSizeScaleDown(sc) {
		for _, s := range state.shardsSetsHelpers[:sc.Status.ShardCount] {
			_, podNames := GetDnsForStatefulSetReplicasSpecified(s.BuildStatefulSet(), clusterName, sc.Status.MongodsPerShardCount)
			membersToScaleDown[s.Name] = podNames[sc.Spec.MongodsPerShardCount:sc.Status.MongodsPerShardCount]
		}
	}

	if len(membersToScaleDown) > 0 {
		if err := prepareScaleDown(omClient, membersToScaleDown, log); err != nil {
			return err
		}
	}
	return nil
}

func isConfigServerScaleDown(sc *mongodb.MongoDbShardedCluster) bool {
	return sc.Spec.ConfigServerCount < sc.Status.ConfigServerCount
}

func isShardsSizeScaleDown(sc *mongodb.MongoDbShardedCluster) bool {
	return sc.Spec.MongodsPerShardCount < sc.Status.MongodsPerShardCount
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func updateOmDeploymentShardedCluster(conn om.Connection, sc *mongodb.MongoDbShardedCluster, state ShardedClusterKubeState, log *zap.SugaredLogger) error {
	err := waitForAgentsToRegister(sc, state, conn, log)
	if err != nil {
		return err
	}

	mongosProcesses := createProcesses(state.mongosSetHelper.BuildStatefulSet(), sc.Spec.ClusterName, sc.Spec.Version, om.ProcessTypeMongos)
	configRs := buildReplicaSetFromStatefulSet(state.configSrvSetHelper.BuildStatefulSet(), sc.Spec.ClusterName, sc.Spec.Version)
	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i, s := range state.shardsSetsHelpers {
		shards[i] = buildReplicaSetFromStatefulSet(s.BuildStatefulSet(), sc.Spec.ClusterName, sc.Spec.Version)
	}

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			if err := d.MergeShardedCluster(sc.Name, mongosProcesses, configRs, shards); err != nil {
				return err
			}
			d.AddMonitoringAndBackup(mongosProcesses[0].HostName(), log)
			processNames = d.GetProcessNames(om.ShardedCluster{}, sc.Name)
			return nil
		}, log,
	)
	if err != nil {
		return err
	}

	if err := conn.WaitForReadyState(processNames, log); err != nil {
		return err
	}

	currentHosts := getAllHosts(sc, sc.Status.MongodbShardedClusterSizeConfig)
	wantedHosts := getAllHosts(sc, sc.Spec.MongodbShardedClusterSizeConfig)

	return calculateDiffAndStopMonitoringHosts(conn, currentHosts, wantedHosts, log)
}

func waitForAgentsToRegister(cluster *mongodb.MongoDbShardedCluster, state ShardedClusterKubeState, conn om.Connection,
	log *zap.SugaredLogger) error {
	if err := waitForRsAgentsToRegister(state.mongosSetHelper.BuildStatefulSet(), cluster.Spec.ClusterName, conn, log); err != nil {
		return err
	}

	if err := waitForRsAgentsToRegister(state.configSrvSetHelper.BuildStatefulSet(), cluster.Spec.ClusterName, conn, log); err != nil {
		return err
	}

	for _, s := range state.shardsSetsHelpers {
		if err := waitForRsAgentsToRegister(s.BuildStatefulSet(), cluster.Spec.ClusterName, conn, log); err != nil {
			return err
		}
	}
	return nil
}

func getMaxShardedClusterSizeConfig(specConfig mongodb.MongodbShardedClusterSizeConfig, statusConfig mongodb.MongodbShardedClusterSizeConfig) mongodb.MongodbShardedClusterSizeConfig {
	return mongodb.MongodbShardedClusterSizeConfig{
		MongosCount:          util.MaxInt(specConfig.MongosCount, statusConfig.MongosCount),
		ConfigServerCount:    util.MaxInt(specConfig.ConfigServerCount, statusConfig.ConfigServerCount),
		MongodsPerShardCount: util.MaxInt(specConfig.MongodsPerShardCount, statusConfig.MongodsPerShardCount),
		ShardCount:           util.MaxInt(specConfig.ShardCount, statusConfig.ShardCount),
	}
}

// getAllHostsFromStatus calculates a list of hosts from the "Status" of the Sharded Cluster
func getAllHosts(c *mongodb.MongoDbShardedCluster, sizeConfig mongodb.MongodbShardedClusterSizeConfig) []string {
	ans := make([]string, 0)

	hosts, _ := GetDnsNames(c.MongosRsName(), c.MongosServiceName(), c.Namespace, c.Spec.ClusterName, sizeConfig.MongosCount)
	ans = append(ans, hosts...)

	hosts, _ = GetDnsNames(c.ConfigRsName(), c.ConfigSrvServiceName(), c.Namespace, c.Spec.ClusterName, sizeConfig.ConfigServerCount)
	ans = append(ans, hosts...)

	for i := 0; i < sizeConfig.ShardCount; i++ {
		hosts, _ = GetDnsNames(c.ShardRsName(i), c.ShardServiceName(), c.Namespace, c.Spec.ClusterName, sizeConfig.MongodsPerShardCount)
		ans = append(ans, hosts...)
	}
	return ans
}
