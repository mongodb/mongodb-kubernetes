package operator

import (
	"errors"

	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/util"
	"go.uber.org/zap"
)

type KubeState struct {
	mongosSetHelper    *StatefulSetHelper
	configSrvSetHelper *StatefulSetHelper
	shardsSetsHelpers  []*StatefulSetHelper
}

func (c *MongoDbController) onAddShardedCluster(obj interface{}) {
	s := obj.(*mongodb.MongoDbShardedCluster)

	log := zap.S().With("sharded cluster", s.Name)

	defer exceptionHandling("Failed to create Mongodb Sharded Cluster", log)

	log.Infow(">> Creating MongoDb Sharded Cluster", "config", s.Spec)

	conn, err := c.doShardedClusterProcessing(nil, s, log)
	if err != nil {
		log.Error(err)
		return
	}

	log.Infof("Created MongoDb Sharded Cluster! %s", completionMessage(conn.BaseUrl(), conn.GroupId()))
}

func (c *MongoDbController) onUpdateShardedCluster(oldObj, newObj interface{}) {
	oldS := oldObj.(*mongodb.MongoDbShardedCluster)
	newS := newObj.(*mongodb.MongoDbShardedCluster)
	log := zap.S().With("sharded cluster", newS.Name)

	defer exceptionHandling("Failed to update Mongodb Sharded Cluster", log)

	if err := validateUpdateShardedCluster(oldS, newS); err != nil {
		log.Error(err)
		return
	}

	log.Infow(">> Updating MongoDb Sharded Cluster", "oldConfig", oldS.Spec, "newConfig", newS.Spec)

	conn, err := c.doShardedClusterProcessing(oldS, newS, log)
	if err != nil {
		log.Error(err)
		return
	}

	log.Infof("Updated MongoDb Sharded Cluster! %s", completionMessage(conn.BaseUrl(), conn.GroupId()))
}

func (c *MongoDbController) doShardedClusterProcessing(o, n *mongodb.MongoDbShardedCluster, log *zap.SugaredLogger) (om.OmConnection, error) {
	conn, podVars, err := c.prepareOmConnection(n.Namespace, n.Spec.Project, n.Spec.Credentials, log)
	if err != nil {
		return nil, err
	}

	kubeState, err := c.buildKubeObjectsForShardedCluster(n, podVars, log)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Failed to build Kubernetes objects: %s", err))
	}

	if err = prepareScaleDownShardedCluster(conn, kubeState, o, n, log); err != nil {
		return nil, errors.New(fmt.Sprintf("Failed to perform scale down preliminary actions: %s", err))
	}

	if err = c.createKubernetesResources(n, kubeState, log); err != nil {
		return nil, errors.New(fmt.Sprintf("Failed to create/update resources in Kubernetes for sharded cluster: %s", err))
	}
	log.Infow("All Kubernetes objects are created/updated, adding the deployment to Ops Manager")

	if err := updateOmDeploymentShardedCluster(conn, o, n, kubeState, log); err != nil {
		return nil, errors.New(fmt.Sprintf("Failed to update OpsManager automation config: %s", err))
	}
	log.Infow("Ops Manager deployment updated successfully")

	return conn, nil
}

func (c *MongoDbController) buildKubeObjectsForShardedCluster(s *mongodb.MongoDbShardedCluster, podVars *PodVars,
	log *zap.SugaredLogger) (KubeState, error) {
	spec := s.Spec

	// note, that mongos statefulset doesn't have state so no PersistentVolumeClaim is created
	mongosBuilder := c.kubeHelper.NewStatefulSetHelper(s).
		SetName(s.MongosRsName()).
		SetService(s.MongosServiceName()).
		SetReplicas(s.Spec.MongosCount).
		SetPodSpec(NewDefaultPodSpecWrapper(s.Spec.MongosPodSpec)).
		SetPodVars(podVars).
		SetLogger(log).
		SetPersistence(util.BooleanRef(false)).
		SetExposedExternally(true)

	defaultConfigSrvSpec := NewDefaultPodSpec()
	defaultConfigSrvSpec.Persistence.SingleConfig.Storage = util.DefaultConfigSrvStorageSize
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: spec.ConfigSrvPodSpec,
		Default:        defaultConfigSrvSpec,
	}
	configBuilder := c.kubeHelper.NewStatefulSetHelper(s).
		SetName(s.ConfigRsName()).
		SetService(s.ConfigSrvServiceName()).
		SetReplicas(s.Spec.ConfigServerCount).
		SetPersistence(s.Spec.Persistent).
		SetPodSpec(podSpec).
		SetPodVars(podVars).
		SetLogger(log).
		SetExposedExternally(false)

	shardsSetHelpers := make([]*StatefulSetHelper, s.Spec.ShardCount)

	for i := 0; i < s.Spec.ShardCount; i++ {
		shardsSetHelpers[i] = c.kubeHelper.NewStatefulSetHelper(s).
			SetName(s.ShardRsName(i)).
			SetService(s.ShardServiceName()).
			SetReplicas(s.Spec.MongodsPerShardCount).
			SetPersistence(s.Spec.Persistent).
			SetPodSpec(NewDefaultPodSpecWrapper(spec.ShardPodSpec)).
			SetPodVars(podVars).
			SetLogger(log)
	}
	return KubeState{mongosSetHelper: mongosBuilder, configSrvSetHelper: configBuilder, shardsSetsHelpers: shardsSetHelpers}, nil
}

func (c *MongoDbController) createKubernetesResources(s *mongodb.MongoDbShardedCluster, state KubeState, log *zap.SugaredLogger) error {
	err := state.mongosSetHelper.CreateOrUpdateInKubernetes()
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to create Mongos Stateful Set: %s", err))
	}

	log.Infow("Created StatefulSet for mongos servers", "name", state.mongosSetHelper.Name, "servers count", state.mongosSetHelper.Replicas)

	err = state.configSrvSetHelper.CreateOrUpdateInKubernetes()
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to create Config Server Stateful Set: %s", err))
	}

	log.Infow("Created StatefulSet for config servers", "name", state.configSrvSetHelper.Name, "servers count", state.configSrvSetHelper.Replicas)

	shardsNames := make([]string, s.Spec.ShardCount)

	for i, s := range state.shardsSetsHelpers {
		shardsNames[i] = s.Name
		err = s.CreateOrUpdateInKubernetes()
		if err != nil {
			return errors.New(fmt.Sprintf("Failed to create Stateful Set for shard %s: %s", s.Name, err))
		}
	}
	log.Infow("Created Stateful Sets for shards in Kubernetes", "shards", shardsNames)

	return nil
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func updateOmDeploymentShardedCluster(omConnection om.OmConnection, old,
	new *mongodb.MongoDbShardedCluster, state KubeState, log *zap.SugaredLogger) error {
	err := waitForAgentsToRegister(new, state, omConnection, log)
	if err != nil {
		return err
	}

	mongosProcesses := createProcesses(state.mongosSetHelper.BuildStatefulSet(), new.Spec.ClusterName, new.Spec.Version, om.ProcessTypeMongos)
	configRs := buildReplicaSetFromStatefulSet(state.configSrvSetHelper.BuildStatefulSet(), new.Spec.ClusterName, new.Spec.Version)
	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i, s := range state.shardsSetsHelpers {
		shards[i] = buildReplicaSetFromStatefulSet(s.BuildStatefulSet(), new.Spec.ClusterName, new.Spec.Version)
	}

	err = omConnection.ReadUpdateDeployment(false,
		func(d om.Deployment) error {
			if err := d.MergeShardedCluster(new.Name, mongosProcesses, configRs, shards); err != nil {
				return err
			}
			d.AddMonitoringAndBackup(mongosProcesses[0].HostName(), log)
			return nil
		},
	)
	if err != nil {
		return err
	}

	if err := deleteHostnamesFromMonitoring(omConnection, getAllHosts(old), getAllHosts(new), log); err != nil {
		return err
	}

	return nil
}

func (c *MongoDbController) onDeleteShardedCluster(obj interface{}) {
	sc := obj.(*mongodb.MongoDbShardedCluster)
	log := zap.S().With("sharded cluster", sc.Name)

	defer exceptionHandling("Failed to delete Mongodb Sharded Cluster", log)

	log.Infow(">> Deleting MongoDb Sharded Cluster", "config", sc.Spec)

	hostsToRemove := getAllHosts(sc)

	conn, _, err := c.prepareOmConnection(sc.Namespace, sc.Spec.Project, sc.Spec.Credentials, log)
	if err != nil {
		log.Error(err)
		return
	}

	err = conn.ReadUpdateDeployment(false,
		func(d om.Deployment) error {
			if err = d.RemoveShardedClusterByName(sc.Name); err != nil {
				return err
			}

			return nil
		},
	)
	if err != nil {
		log.Errorf("Failed to update Ops Manager automation config: %s", err)
	}

	err = om.StopBackupIfEnabled(conn, sc.Name, om.ShardedClusterType, log)
	if err != nil {
		log.Errorf("Failed to disable backup for sharded cluster: %s", err)
	}

	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err := om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		log.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
		return
	}

	log.Info("Removed MongoDb Sharded Cluster!")
}

func prepareScaleDownShardedCluster(omClient om.OmConnection, state KubeState, old, new *mongodb.MongoDbShardedCluster,
	log *zap.SugaredLogger) error {
	if old == nil {
		return nil
	}
	membersToScaleDown := make(map[string][]string)
	clusterName := new.Spec.ClusterName

	if new.Spec.ConfigServerCount < old.Spec.ConfigServerCount {
		_, podNames := GetDnsForStatefulSetReplicasSpecified(state.configSrvSetHelper.BuildStatefulSet(), clusterName, old.Spec.ConfigServerCount)
		membersToScaleDown[state.configSrvSetHelper.Name] = podNames[new.Spec.ConfigServerCount:old.Spec.ConfigServerCount]
	}

	if new.Spec.MongodsPerShardCount < old.Spec.MongodsPerShardCount {
		for _, s := range state.shardsSetsHelpers[:old.Spec.ShardCount] {
			_, podNames := GetDnsForStatefulSetReplicasSpecified(s.BuildStatefulSet(), clusterName, old.Spec.MongodsPerShardCount)
			membersToScaleDown[s.Name] = podNames[new.Spec.MongodsPerShardCount:old.Spec.MongodsPerShardCount]
		}
	}

	if len(membersToScaleDown) > 0 {
		if err := prepareScaleDown(omClient, membersToScaleDown, log); err != nil {
			return err
		}
	}
	return nil
}

func validateUpdateShardedCluster(oldSpec, newSpec *mongodb.MongoDbShardedCluster) error {
	if newSpec.Namespace != oldSpec.Namespace {
		return errors.New("Namespace cannot change for existing object")
	}

	if newSpec.Spec.ClusterName != oldSpec.Spec.ClusterName {
		return errors.New("Cluster Name cannot change for existing object")
	}

	return nil
}

// getAllHosts returns all hosts for sharded cluster for mongos/config/shards.
func getAllHosts(c *mongodb.MongoDbShardedCluster) []string {
	if c == nil {
		return []string{}
	}
	ans := make([]string, 0)

	hosts, _ := GetDnsNames(c.MongosRsName(), c.MongosServiceName(), c.Namespace, c.Spec.ClusterName, c.Spec.MongosCount)
	ans = append(ans, hosts...)
	hosts, _ = GetDnsNames(c.ConfigRsName(), c.ConfigSrvServiceName(), c.Namespace, c.Spec.ClusterName, c.Spec.ConfigServerCount)
	ans = append(ans, hosts...)

	for i := 0; i < c.Spec.ShardCount; i++ {
		hosts, _ = GetDnsNames(c.ShardRsName(i), c.ShardServiceName(), c.Namespace, c.Spec.ClusterName, c.Spec.MongodsPerShardCount)
		ans = append(ans, hosts...)
	}
	return ans
}

func waitForAgentsToRegister(cluster *mongodb.MongoDbShardedCluster, state KubeState, omConnection om.OmConnection,
	log *zap.SugaredLogger) error {
	if err := waitForRsAgentsToRegister(state.mongosSetHelper.BuildStatefulSet(), cluster.Spec.ClusterName, omConnection, log); err != nil {
		return err
	}
	if err := waitForRsAgentsToRegister(state.configSrvSetHelper.BuildStatefulSet(), cluster.Spec.ClusterName, omConnection, log); err != nil {
		return err
	}

	for _, s := range state.shardsSetsHelpers {
		if err := waitForRsAgentsToRegister(s.BuildStatefulSet(), cluster.Spec.ClusterName, omConnection, log); err != nil {
			return err
		}
	}
	return nil
}
