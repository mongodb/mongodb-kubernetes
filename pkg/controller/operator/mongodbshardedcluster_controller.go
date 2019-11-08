package operator

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
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
	sc := &mdbv1.MongoDB{}

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

	conn, status := r.doShardedClusterProcessing(sc, log)
	if !status.isOk() {
		return status.updateStatus(sc, r.ReconcileCommonController, log)
	}

	log.Infof("Finished reconciliation for Sharded Cluster! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return status.updateStatus(sc, r.ReconcileCommonController, log, DeploymentLink(conn.BaseURL(), conn.GroupID()))
}

// implements all the logic to do the sharded cluster thing
func (r *ReconcileMongoDbShardedCluster) doShardedClusterProcessing(obj interface{}, log *zap.SugaredLogger) (om.Connection, reconcileStatus) {
	log.Info("ShardedCluster.doShardedClusterProcessing")
	sc := obj.(*mdbv1.MongoDB)
	projectConfig, err := r.kubeHelper.readProjectConfig(sc.Namespace, sc.Spec.GetProject())
	if err != nil {
		return nil, failed("error reading project %s", err)
	}

	sc.Spec.SetParametersFromConfigMap(projectConfig)

	podVars := &PodVars{}
	conn, err := r.prepareConnection(objectKey(sc.Namespace, sc.Name), sc.Spec.ConnectionSpec, podVars, log)
	if err != nil {
		return nil, failedErr(err)
	}

	reconcileResult := checkIfHasExcessProcesses(conn, sc, log)
	if !reconcileResult.isOk() {
		return nil, reconcileResult
	}

	authSpec := sc.Spec.Security.Authentication
	if authSpec.Enabled && authSpec.IsX509Enabled() && !sc.Spec.GetTLSConfig().Enabled {
		return nil, failedValidation("cannot have a non-tls deployment when x509 authentication is enabled")
	}

	kubeState := r.buildKubeObjectsForShardedCluster(sc, podVars, projectConfig, log)

	if err = prepareScaleDownShardedCluster(conn, kubeState, sc, log); err != nil {
		return nil, failed("failed to perform scale down preliminary actions: %s", err)
	}

	if status := validateMongoDBResource(sc, conn); !status.isOk() {
		return nil, status
	}

	if status := r.ensureSSLCertificates(sc, kubeState, log); !status.isOk() {
		return nil, status
	}

	if status := r.ensureX509InKubernetes(sc, kubeState, log); !status.isOk() {
		return nil, status
	}

	status := runInGivenOrder(anyStatefulSetHelperNeedsToPublishState(kubeState, log),
		func() reconcileStatus {
			return updateOmDeploymentShardedCluster(conn, sc, kubeState, log).onErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() reconcileStatus {
			return r.createKubernetesResources(sc, kubeState, log).onErrorPrepend("Failed to create/update (Kubernetes reconciliation phase):")
		})

	if !status.isOk() {
		return nil, status
	}

	r.removeUnusedStatefulsets(sc, kubeState, log)
	return conn, reconcileResult

}

// anyStatefulSetHelperNeedsToPublishState checks to see if any stateful set helper part
// of the given sharded cluster needs to publish state to Ops Manager before updating Kubernetes resources
func anyStatefulSetHelperNeedsToPublishState(kubeState ShardedClusterKubeState, log *zap.SugaredLogger) bool {
	allHelpers := getAllStatefulSetHelpers(kubeState)
	for _, stsHelper := range allHelpers {
		if stsHelper.needToPublishStateFirst(log) {
			return true
		}
	}
	return false
}

func (r *ReconcileMongoDbShardedCluster) ensureX509InKubernetes(sc *mdbv1.MongoDB, kubeState ShardedClusterKubeState, log *zap.SugaredLogger) reconcileStatus {
	authEnabled := sc.Spec.Security.Authentication.Enabled
	usingX509 := sc.Spec.Security.Authentication.GetAgentMechanism() == util.X509
	if authEnabled && usingX509 {
		authModes := sc.Spec.Security.Authentication.Modes
		useCustomCA := sc.Spec.GetTLSConfig().CA != ""
		successful, err := r.ensureX509AgentCertsForMongoDBResource(authModes, useCustomCA, sc.Namespace, log)
		if err != nil {
			return failedErr(err)
		}
		if !successful {
			return pending("Agent certs have not yet been approved")
		}
		if !sc.Spec.Security.TLSConfig.Enabled {
			return failed("authentication mode for project is x509 but this MDB resource is not TLS enabled")
		} else if !r.doAgentX509CertsExist(sc.Namespace) {
			return pending("agent x509 certificates have not yet been created")
		}

		if sc.Spec.Security.Authentication.InternalCluster == util.X509 {
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
				return failed("failed ensuring internal cluster authentication certs %s", errors[0])
			} else if !allSuccessful {
				return pending("not all internal cluster authentication certs have been approved by Kubernetes CA")
			}
		}

	}
	return ok()
}

func (r *ReconcileMongoDbShardedCluster) removeUnusedStatefulsets(sc *mdbv1.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger) {
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

func (r *ReconcileMongoDbShardedCluster) ensureSSLCertificates(s *mdbv1.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger) reconcileStatus {
	tlsConfig := s.Spec.GetTLSConfig()

	if tlsConfig == nil || !s.Spec.GetTLSConfig().Enabled {
		return ok()
	}

	var status reconcileStatus
	status = ok()
	status = status.merge(r.kubeHelper.ensureSSLCertsForStatefulSet(state.mongosSetHelper, log))
	status = status.merge(r.kubeHelper.ensureSSLCertsForStatefulSet(state.configSrvSetHelper, log))

	for _, helper := range state.shardsSetsHelpers {
		status = status.merge(r.kubeHelper.ensureSSLCertsForStatefulSet(helper, log))
	}

	return status
}

// createKubernetesResources creates all Kubernetes objects that are specified in 'state' parameter.
// This function returns errorStatus if any errors occured or pendingStatus if the statefulsets are not
// ready yet
// Note, that it doesn't remove any existing shards - this will be done later
func (r *ReconcileMongoDbShardedCluster) createKubernetesResources(s *mdbv1.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger) reconcileStatus {
	err := state.mongosSetHelper.CreateOrUpdateInKubernetes()
	if err != nil {
		return failed("Failed to create Mongos Stateful Set: %s", err)
	}

	if !r.kubeHelper.isStatefulSetUpdated(state.mongosSetHelper.Namespace, state.mongosSetHelper.Name, log) {
		return pending("StatefulSet %s/%s is still pending to start/update", state.mongosSetHelper.Namespace, state.mongosSetHelper.Name)
	}

	log.Infow("Created/updated StatefulSet for mongos servers", "name", state.mongosSetHelper.Name, "servers count", state.mongosSetHelper.Replicas)

	err = state.configSrvSetHelper.CreateOrUpdateInKubernetes()
	if err != nil {
		return failed("Failed to create Config Server Stateful Set: %s", err)
	}
	if !r.kubeHelper.isStatefulSetUpdated(state.configSrvSetHelper.Namespace, state.configSrvSetHelper.Name, log) {
		return pending("StatefulSet %s/%s is still pending to start/update", state.configSrvSetHelper.Namespace, state.configSrvSetHelper.Name)
	}

	log.Infow("Created/updated StatefulSet for config servers", "name", state.configSrvSetHelper.Name, "servers count", state.configSrvSetHelper.Replicas)

	shardsNames := make([]string, s.Spec.ShardCount)

	for i, s := range state.shardsSetsHelpers {
		shardsNames[i] = s.Name
		err = s.CreateOrUpdateInKubernetes()
		if err != nil {
			return failed("Failed to create Stateful Set for shard %s: %s", s.Name, err)
		}
		if !r.kubeHelper.isStatefulSetUpdated(s.Namespace, s.Name, log) {
			return pending("StatefulSet %s/%s is still pending to start/update", s.Namespace, s.Name)
		}
	}

	log.Infow("Created/updated Stateful Sets for shards in Kubernetes", "shards", shardsNames)

	return ok()
}

func (r *ReconcileMongoDbShardedCluster) buildKubeObjectsForShardedCluster(s *mdbv1.MongoDB, podVars *PodVars, projectConfig *mdbv1.ProjectConfig, log *zap.SugaredLogger) ShardedClusterKubeState {
	// 1. Create the mongos StatefulSet
	mongosBuilder := r.kubeHelper.NewStatefulSetHelper(s).
		SetName(s.MongosRsName()).
		SetService(s.ServiceName()).
		SetReplicas(s.Spec.MongosCount).
		SetPodSpec(NewDefaultPodSpecWrapper(*s.Spec.MongosPodSpec)).
		SetPodVars(podVars).
		SetLogger(log).
		SetPersistence(util.BooleanRef(false)).
		SetTLS(s.Spec.GetTLSConfig()).
		SetProjectConfig(*projectConfig).
		SetSecurity(s.Spec.Security)

	// 2. Create a Config Server StatefulSet
	defaultConfigSrvSpec := NewDefaultPodSpec()
	defaultConfigSrvSpec.Persistence.SingleConfig.Storage = util.DefaultConfigSrvStorageSize
	podSpec := mdbv1.PodSpecWrapper{
		MongoDbPodSpec: *s.Spec.ConfigSrvPodSpec,
		Default:        defaultConfigSrvSpec,
	}
	configBuilder := r.kubeHelper.NewStatefulSetHelper(s).
		SetName(s.ConfigRsName()).
		SetService(s.ConfigSrvServiceName()).
		SetReplicas(s.Spec.ConfigServerCount).
		SetPodSpec(podSpec).
		SetPodVars(podVars).
		SetLogger(log).
		SetTLS(s.Spec.GetTLSConfig()).
		SetProjectConfig(*projectConfig).
		SetSecurity(s.Spec.Security)

	// 3. Creates a StatefulSet for each shard in the cluster
	shardsSetHelpers := make([]*StatefulSetHelper, s.Spec.ShardCount)
	for i := 0; i < s.Spec.ShardCount; i++ {
		shardsSetHelpers[i] = r.kubeHelper.NewStatefulSetHelper(s).
			SetName(s.ShardRsName(i)).
			SetService(s.ShardServiceName()).
			SetReplicas(s.Spec.MongodsPerShardCount).
			SetPodSpec(NewDefaultPodSpecWrapper(*s.Spec.ShardPodSpec)).
			SetPodVars(podVars).
			SetLogger(log).
			SetTLS(s.Spec.GetTLSConfig()).
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
	sc := obj.(*mdbv1.MongoDB)

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
	err = c.Watch(&source.Kind{Type: &mdbv1.MongoDB{}}, &eventHandler, predicatesFor(mdbv1.ShardedCluster))
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

func prepareScaleDownShardedCluster(omClient om.Connection, state ShardedClusterKubeState, sc *mdbv1.MongoDB, log *zap.SugaredLogger) error {
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

func isConfigServerScaleDown(sc *mdbv1.MongoDB) bool {
	return sc.Spec.ConfigServerCount < sc.Status.ConfigServerCount
}

func isShardsSizeScaleDown(sc *mdbv1.MongoDB) bool {
	return sc.Spec.MongodsPerShardCount < sc.Status.MongodsPerShardCount
}

// updateOmDeploymentShardedCluster performs OM registration operation for the sharded cluster. So the changes will be finally propagated
// to automation agents in containers
// Note that the process may have two phases (if shards number is decreased):
// phase 1: "drain" the shards: remove them from sharded cluster, put replica set names to "draining" array, not remove
// replica sets and processes, wait for agents to reach the goal
// phase 2: remove the "junk" replica sets and their processes, wait for agents to reach the goal.
// The logic is designed to be idempotent: if the reconciliation is retried the controller will never skip the phase 1
// until the agents have performed draining
func updateOmDeploymentShardedCluster(conn om.Connection, sc *mdbv1.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger) reconcileStatus {
	err := waitForAgentsToRegister(sc, state, conn, log)
	if err != nil {
		return failedErr(err)
	}

	dep, err := conn.ReadDeployment()
	if err != nil {
		return failedErr(err)
	}

	processNames := dep.GetProcessNames(om.ShardedCluster{}, sc.Name)

	status, shardsRemoving := publishDeployment(conn, sc, state, log, &processNames, false)

	if !status.isOk() {
		return status
	}

	if err = om.WaitForReadyState(conn, processNames, log); err != nil {
		if shardsRemoving {
			return pending("automation agents haven't reached READY state: shards removal in progress")
		}
		return failedErr(err)
	}

	if shardsRemoving {
		log.Infof("Some shards were removed from the sharded cluster, we need to remove them from the deployment completely")
		status, shardsRemoving = publishDeployment(conn, sc, state, log, &processNames, true)
		if !status.isOk() {
			return status
		}

		if err = om.WaitForReadyState(conn, processNames, log); err != nil {
			return failed("automation agents haven't reached READY state while cleaning replica set and processes: %s", err)
		}
	}

	currentHosts := getAllHosts(sc, sc.Status.MongodbShardedClusterSizeConfig)
	wantedHosts := getAllHosts(sc, sc.Spec.MongodbShardedClusterSizeConfig)

	if err = calculateDiffAndStopMonitoringHosts(conn, currentHosts, wantedHosts, log); err != nil {
		return failedErr(err)
	}
	log.Info("Updated Ops Manager for sharded cluster")
	return ok()
}

func publishDeployment(conn om.Connection, sc *mdbv1.MongoDB, state ShardedClusterKubeState, log *zap.SugaredLogger,
	processNames *[]string, finalizing bool) (reconcileStatus, bool) {
	mongosProcesses := createProcesses(
		state.mongosSetHelper.BuildStatefulSet(),
		om.ProcessTypeMongos,
		sc,
	)

	configRs := buildReplicaSetFromStatefulSet(state.configSrvSetHelper.BuildStatefulSet(), sc)

	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i, s := range state.shardsSetsHelpers {
		shards[i] = buildReplicaSetFromStatefulSet(s.BuildStatefulSet(), sc)
	}

	status, additionalReconciliationRequired := updateOmAuthentication(conn, *processNames, sc, log)
	if !status.isOk() {
		return status, false
	}

	shardsRemoving := false
	err := conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			// it is not possible to disable internal cluster authentication once enabled
			allProcesses := getAllProcesses(shards, configRs, mongosProcesses)
			if sc.Spec.Security.Authentication.InternalCluster == "" && d.ExistingProcessesHaveInternalClusterAuthentication(allProcesses) {
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
			d.AddMonitoringAndBackup(mongosProcesses[0].HostName(), log)
			d.ConfigureTLS(sc.Spec.GetTLSConfig())
			d.ConfigureInternalClusterAuthentication(*processNames, sc.Spec.Security.Authentication.InternalCluster)

			processes := d.GetProcessNames(om.ShardedCluster{}, sc.Name)
			*processNames = processes
			return nil
		},
		log,
	)

	if err != nil {
		return failedErr(err), shardsRemoving
	}

	if err := om.WaitForReadyState(conn, *processNames, log); err != nil {
		return failedErr(err), shardsRemoving
	}

	if additionalReconciliationRequired {
		return pending("Performing multi stage reconciliation"), shardsRemoving
	}

	return ok(), shardsRemoving
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

func waitForAgentsToRegister(cluster *mdbv1.MongoDB, state ShardedClusterKubeState, conn om.Connection,
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
