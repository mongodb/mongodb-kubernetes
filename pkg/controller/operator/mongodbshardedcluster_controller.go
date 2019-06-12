package operator

import (
	"fmt"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
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
}

func newShardedClusterReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *ReconcileMongoDbShardedCluster {
	return &ReconcileMongoDbShardedCluster{newReconcileCommonController(mgr, omFunc)}
}

func (r *ReconcileMongoDbShardedCluster) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("ShardedCluster", request.NamespacedName)
	sc := &mongodb.MongoDB{}

	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatusFailed(sc, fmt.Sprintf("Failed to reconcile Sharded Cluster: %s", err), log)
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

	processingResult := r.doShardedClusterProcessing(sc, log)
	if processingResult.isFailure {
		return r.updateStatusFailed(sc, processingResult.msg, log)
	} else if processingResult.shouldGoIntoPending {
		log.Infof(processingResult.msg)
		return r.updateStatusPending(sc, processingResult.msg)
	}

	conn := processingResult.connection

	r.updateStatusSuccessful(sc, log, DeploymentLink(conn.BaseURL(), conn.GroupID()))
	log.Infof("Finished reconciliation for Sharded Cluster! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return reconcile.Result{}, nil
}

// processingResult contains all the fields required to determine the outcome
// of the sharded cluster processing, and which state the resource should enter next
type processingResult struct {
	connection                     om.Connection
	msg                            string
	isFailure, shouldGoIntoPending bool
}

// implements all the logic to do the sharded cluster thing
func (r *ReconcileMongoDbShardedCluster) doShardedClusterProcessing(obj interface{}, log *zap.SugaredLogger) processingResult {
	log.Info("ShardedCluster.doShardedClusterProcessing")
	sc := obj.(*mongodb.MongoDB)
	podVars := &PodVars{}
	conn, err := r.prepareConnection(objectKey(sc.Namespace, sc.Name), sc.Spec.ConnectionSpec, podVars, log)
	if err != nil {
		return processingResult{isFailure: true, msg: err.Error()}
	}

	projectConfig, err := r.kubeHelper.readProjectConfig(sc.Namespace, sc.Spec.Project)
	if err != nil {
		return processingResult{isFailure: true, msg: fmt.Sprintf("error reading project %s", err)}
	}

	kubeState := r.buildKubeObjectsForShardedCluster(sc, podVars, projectConfig, log)

	if err = prepareScaleDownShardedCluster(conn, kubeState, sc, log); err != nil {
		return processingResult{isFailure: true, msg: fmt.Sprintf("Failed to perform scale down preliminary actions: %s", err)}
	}

	if wasSuccessful, err := r.ensureSSLCertificates(sc, kubeState, log); err != nil {
		return processingResult{isFailure: true, msg: err.Error()}
	} else if sc.Spec.GetTLSConfig().Enabled && !wasSuccessful {
		return processingResult{isFailure: false, shouldGoIntoPending: true, msg: "Not all certificates have been approved by Kubernetes CA"}
	}

	if projectConfig.AuthMode == util.X509 {
		if !sc.Spec.Security.TLSConfig.Enabled {
			return processingResult{isFailure: false, msg: "authentication mode for project is x509 but this MDB resource is not TLS enabled"}
		} else if !r.doAgentX509CertsExist(sc.Namespace) {
			return processingResult{isFailure: false, shouldGoIntoPending: true, msg: "Agent x509 certificates have not yet been created"}
		}

		if sc.Spec.Security.ClusterAuthMode == util.X509 {
			errors := make([]error, 0)
			allSuccessful := true
			for _, helper := range getAllStatefulSetHelpers(kubeState) {
				if success, err := r.ensureInternalClusterCerts(helper, log); err != nil {
					errors = append(errors, err)
				} else if !success {
					allSuccessful = false
				}
			}
			// fail only after creating all CSRs
			if len(errors) > 0 {
				return processingResult{isFailure: true, msg: fmt.Sprintf("failed ensuring internal cluster authentication certs %s", errors[0])}
			} else if !allSuccessful {
				return processingResult{isFailure: false, shouldGoIntoPending: true, msg: "Not all internal cluster authentication certs have been approved by Kubernetes CA"}
			}
		}

	} else {
		// this means the user has disabled x509 at the project level, but the resource is still configured to use x509 cluster authentication
		// as we don't have a status on the ConfigMap, we can inform the user in the status of the resource.
		if sc.Spec.Security.ClusterAuthMode == util.X509 {
			return processingResult{isFailure: true, msg: "This deployment has clusterAuthenticationMode set to x509, ensure the ConfigMap for this project is configured to enable x509"}
		}
	}

	if err = r.createKubernetesResources(sc, kubeState, log); err != nil {
		return processingResult{isFailure: true, msg: fmt.Sprintf("Failed to create/update resources in Kubernetes for sharded cluster: %s", err)}
	}
	log.Infow("All Kubernetes objects are created/updated, adding the deployment to Ops Manager")

	if err := updateOmDeploymentShardedCluster(conn, sc, kubeState, log); err != nil {
		return processingResult{isFailure: true, msg: fmt.Sprintf("Failed to update OpsManager automation config: %s", err)}
	}

	log.Infow("Ops Manager deployment updated successfully")
	r.removeUnusedStatefulsets(sc, kubeState, log)
	return processingResult{connection: conn}

}

func (r *ReconcileMongoDbShardedCluster) removeUnusedStatefulsets(sc *mongodb.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger) {
	statefulsetsToRemove := sc.Status.MongodbShardedClusterSizeConfig.ShardCount - sc.Spec.MongodbShardedClusterSizeConfig.ShardCount
	shardsCount := sc.Status.MongodbShardedClusterSizeConfig.ShardCount

	// we iterate over last 'statefulsetsToRemove' shards if any
	for i := shardsCount - statefulsetsToRemove; i < shardsCount; i++ {
		key := objectKey(sc.Namespace, sc.ShardRsName(i))
		err := r.kubeHelper.deleteStatefulSet(key)
		if err != nil {
			// Most of all the error won't be recoverable, also our sharded cluster is in good shape - we can just warn
			// the error and leave the cleanup work for the admins
			log.Warnf("Failed to delete the statefulset %s: %s", key, err)
		}
		log.Infof("Removed statefulset %s as it's was removed from sharded cluster", key)
	}
}

func (r *ReconcileMongoDbShardedCluster) ensureSSLCertificates(s *mongodb.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger) (bool, error) {
	tlsConfig := s.Spec.GetTLSConfig()

	if tlsConfig == nil || !s.Spec.GetTLSConfig().Enabled {
		return true, nil
	}

	var lastErr error
	errorHappened := false
	allSucceeded := true
	if successful, err := r.kubeHelper.ensureSSLCertsForStatefulSet(state.mongosSetHelper, log); err != nil {
		errorHappened = true
		lastErr = err
	} else if !successful {
		allSucceeded = false
	}
	if successful, err := r.kubeHelper.ensureSSLCertsForStatefulSet(state.configSrvSetHelper, log); err != nil {
		errorHappened = true
		lastErr = err
	} else if !successful {
		allSucceeded = false
	}
	for _, s := range state.shardsSetsHelpers {
		if successful, err := r.kubeHelper.ensureSSLCertsForStatefulSet(s, log); err != nil {
			errorHappened = true
			lastErr = err
		} else if !successful {
			allSucceeded = false
		}
	}

	if errorHappened {
		return false, lastErr
	}

	return allSucceeded, nil
}

// createKubernetesResources creates all Kubernetes objects that are specified in 'state' parameter
// Note, that it doesn't remove any existing shards - this will be done later
func (r *ReconcileMongoDbShardedCluster) createKubernetesResources(s *mongodb.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger) error {
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

func (r *ReconcileMongoDbShardedCluster) buildKubeObjectsForShardedCluster(s *mongodb.MongoDB, podVars *PodVars, projectConfig *ProjectConfig, log *zap.SugaredLogger) ShardedClusterKubeState {
	// 1. Create the mongos StatefulSet
	mongosBuilder := r.kubeHelper.NewStatefulSetHelper(s).
		SetName(s.MongosRsName()).
		SetService(s.ServiceName()).
		SetReplicas(s.Spec.MongosCount).
		SetPodSpec(NewDefaultPodSpecWrapper(*s.Spec.MongosPodSpec)).
		SetPodVars(podVars).
		SetLogger(log).
		SetPersistence(util.BooleanRef(false)).
		SetExposedExternally(s.Spec.ExposedExternally).
		SetTLS(s.Spec.GetTLSConfig()).
		SetClusterName(s.Spec.ClusterName).
		SetProjectConfig(*projectConfig).
		SetSecurity(s.Spec.Security)

	// 2. Create a Config Server StatefulSet
	defaultConfigSrvSpec := NewDefaultPodSpec()
	defaultConfigSrvSpec.Persistence.SingleConfig.Storage = util.DefaultConfigSrvStorageSize
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: *s.Spec.ConfigSrvPodSpec,
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
		SetTLS(s.Spec.GetTLSConfig()).
		SetClusterName(s.Spec.ClusterName).
		SetProjectConfig(*projectConfig).
		SetSecurity(s.Spec.Security)

	// 3. Creates a StatefulSet for each shard in the cluster
	shardsSetHelpers := make([]*StatefulSetHelper, s.Spec.ShardCount)
	for i := 0; i < s.Spec.ShardCount; i++ {
		shardsSetHelpers[i] = r.kubeHelper.NewStatefulSetHelper(s).
			SetName(s.ShardRsName(i)).
			SetService(s.ShardServiceName()).
			SetReplicas(s.Spec.MongodsPerShardCount).
			SetPersistence(s.Spec.Persistent).
			SetPodSpec(NewDefaultPodSpecWrapper(*s.Spec.ShardPodSpec)).
			SetPodVars(podVars).
			SetLogger(log).
			SetTLS(s.Spec.GetTLSConfig()).
			SetClusterName(s.Spec.ClusterName).
			SetProjectConfig(*projectConfig).
			SetSecurity(s.Spec.Security)
	}

	return ShardedClusterKubeState{
		mongosSetHelper:    mongosBuilder,
		configSrvSetHelper: configBuilder,
		shardsSetsHelpers:  shardsSetHelpers,
	}

}

// delete tries to complete a Deletion reconciliation event
func (r *ReconcileMongoDbShardedCluster) delete(obj interface{}, log *zap.SugaredLogger) error {
	sc := obj.(*mongodb.MongoDB)

	conn, err := r.prepareConnection(objectKey(sc.Namespace, sc.Name), sc.Spec.ConnectionSpec, nil, log)
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
		getMutex(conn.GroupName(), conn.OrgID()),
		log,
	)
	if err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
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

func AddShardedClusterController(mgr manager.Manager) error {
	reconciler := newShardedClusterReconciler(mgr, om.NewOpsManagerConnection)
	options := controller.Options{Reconciler: reconciler}
	c, err := controller.New(util.MongoDbShardedClusterController, mgr, options)
	if err != nil {
		return err
	}

	// watch for changes to sharded cluster MongoDB resources
	eventHandler := MongoDBResourceEventHandler{reconciler: reconciler}
	err = c.Watch(&source.Kind{Type: &mongodb.MongoDB{}}, &eventHandler, predicatesFor(mongodb.ShardedCluster))
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

func prepareScaleDownShardedCluster(omClient om.Connection, state ShardedClusterKubeState, sc *mongodb.MongoDB, log *zap.SugaredLogger) error {
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

func isConfigServerScaleDown(sc *mongodb.MongoDB) bool {
	return sc.Spec.ConfigServerCount < sc.Status.ConfigServerCount
}

func isShardsSizeScaleDown(sc *mongodb.MongoDB) bool {
	return sc.Spec.MongodsPerShardCount < sc.Status.MongodsPerShardCount
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
// Note that the process may have two phases (if shards number is decreased):
// phase 1: "drain" the shards: remove them from sharded cluster, put replica set names to "draining" array, not remove
// replica sets and processes, wait for agents to reach the goal
// phase 2: remove the "junk" replica sets and their processes, wait for agents to reach the goal.
// The logic is designed to be idempotent: if the reconciliation is retried the controller will never skip the phase 1
// until the agents have performed draining
func updateOmDeploymentShardedCluster(conn om.Connection, sc *mongodb.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger) error {
	err := waitForAgentsToRegister(sc, state, conn, log)
	if err != nil {
		return err
	}

	shardsRemoving := false
	processNames := make([]string, 0)
	err, shardsRemoving = publishDeployment(conn, sc, state, log, &processNames, false)
	if err != nil {
		return err
	}

	if err = om.WaitForReadyState(conn, processNames, log); err != nil {
		if shardsRemoving {
			// todo this should result in Pending status
			return fmt.Errorf("automation agents haven't reached READY state: shards removal in progress")
		}
		return err
	}

	if shardsRemoving {
		log.Infof("Some shards were removed from the sharded cluster, we need to remove them from the deployment completely")
		err, shardsRemoving = publishDeployment(conn, sc, state, log, &processNames, true)
		if err != nil {
			return err
		}

		if err = om.WaitForReadyState(conn, processNames, log); err != nil {
			return fmt.Errorf("automation agents haven't reached READY state while cleaning replica set and processes")
		}
	}

	currentHosts := getAllHosts(sc, sc.Status.MongodbShardedClusterSizeConfig)
	wantedHosts := getAllHosts(sc, sc.Spec.MongodbShardedClusterSizeConfig)

	return calculateDiffAndStopMonitoringHosts(conn, currentHosts, wantedHosts, log)
}

func publishDeployment(conn om.Connection, sc *mongodb.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger,
	processNames *[]string, finalizing bool) (error, bool) {
	mongosProcesses := createProcesses(
		state.mongosSetHelper.BuildStatefulSet(),
		om.ProcessTypeMongos,
		sc,
		log,
	)

	configRs := buildReplicaSetFromStatefulSet(state.configSrvSetHelper.BuildStatefulSet(), sc, log)

	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i, s := range state.shardsSetsHelpers {
		shards[i] = buildReplicaSetFromStatefulSet(s.BuildStatefulSet(), sc, log)
	}

	shardsRemoving := false
	err := conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			// it is not possible to disable internal cluster authentication once enabled
			allProcesses := getAllProcesses(shards, configRs, mongosProcesses)
			if sc.Spec.Security.ClusterAuthMode == "" && d.ExistingProcessesHaveInternalClusterAuthentication(allProcesses) {
				return fmt.Errorf("cannot disable x509 internal cluster authentication")
			}
			var err error
			if shardsRemoving, err = d.MergeShardedCluster(sc.Name, mongosProcesses, configRs, shards, finalizing); err != nil {
				return err
			}
			d.AddMonitoringAndBackup(mongosProcesses[0].HostName(), log)
			d.ConfigureTLS(sc.Spec.GetTLSConfig())

			processes := d.GetProcessNames(om.ShardedCluster{}, sc.Name)
			*processNames = processes
			return nil
		},
		getMutex(conn.GroupName(), conn.OrgID()),
		log,
	)
	return err, shardsRemoving
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

func waitForAgentsToRegister(cluster *mongodb.MongoDB, state ShardedClusterKubeState, conn om.Connection,
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
func getAllHosts(c *mongodb.MongoDB, sizeConfig mongodb.MongodbShardedClusterSizeConfig) []string {
	ans := make([]string, 0)

	hosts, _ := GetDNSNames(c.MongosRsName(), c.ServiceName(), c.Namespace, c.Spec.ClusterName, sizeConfig.MongosCount)
	ans = append(ans, hosts...)

	hosts, _ = GetDNSNames(c.ConfigRsName(), c.ConfigSrvServiceName(), c.Namespace, c.Spec.ClusterName, sizeConfig.ConfigServerCount)
	ans = append(ans, hosts...)

	for i := 0; i < sizeConfig.ShardCount; i++ {
		hosts, _ = GetDNSNames(c.ShardRsName(i), c.ShardServiceName(), c.Namespace, c.Spec.ClusterName, sizeConfig.MongodsPerShardCount)
		ans = append(ans, hosts...)
	}
	return ans
}

// getAllStatefulSetHelpers returns a list of all StatefulSetHelpers that
// make up a Sharded Cluster
func getAllStatefulSetHelpers(kubeState ShardedClusterKubeState) []*StatefulSetHelper {
	stsHelpers := make([]*StatefulSetHelper, 0)
	stsHelpers = append(stsHelpers, kubeState.shardsSetsHelpers...)
	stsHelpers = append(stsHelpers, kubeState.mongosSetHelper)
	stsHelpers = append(stsHelpers, kubeState.configSrvSetHelper)
	return stsHelpers
}
