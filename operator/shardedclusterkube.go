package operator

import (
	"errors"

	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1beta1"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
)

type KubeState struct {
	mongosSet    *appsv1.StatefulSet
	configSrvSet *appsv1.StatefulSet
	shardsSets   []*appsv1.StatefulSet
}

func (c *MongoDbController) onAddShardedCluster(obj interface{}) {
	s := obj.(*mongodb.MongoDbShardedCluster)

	log := zap.S().With("sharded cluster", s.Name)

	log.Infow("Creating MongoDbShardedCluster", "config", s.Spec)

	if err := c.doShardedClusterProcessing(nil, s, log); err != nil {
		log.Error(err)
		return
	}

	log.Info("Created!")
}

func (c *MongoDbController) onUpdateShardedCluster(oldObj, newObj interface{}) {
	oldS := oldObj.(*mongodb.MongoDbShardedCluster)
	newS := newObj.(*mongodb.MongoDbShardedCluster)
	log := zap.S().With("sharded cluster", newS.Name)

	if err := validateUpdateShardedCluster(oldS, newS); err != nil {
		log.Error(err)
		return
	}

	log.Infow("Updating MongoDbShardedCluster", "oldConfig", oldS.Spec, "newConfig", newS.Spec)

	if err := c.doShardedClusterProcessing(oldS, newS, log); err != nil {
		log.Error(err)
		return
	}

	log.Info("Updated!")
}

func (c *MongoDbController) doShardedClusterProcessing(o, n *mongodb.MongoDbShardedCluster, log *zap.SugaredLogger) error {
	conn, err := c.getOmConnection(n.Namespace, n.Spec.Project, n.Spec.Credentials)
	if err != nil {
		return err
	}

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(conn, n.Namespace, log)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to generate/get agent key: %s", err))
	}

	kubeState, err := c.buildKubeObjectsForShardedCluster(n, agentKeySecretName, log)

	if err != nil {
		return errors.New(fmt.Sprintf("Error creating Kubernetes objects for sharded cluster: %s", err))
	}
	log.Infow("All Kubernetes objects are created/updated, adding the deployment to Ops Manager")

	if err := c.updateOmDeploymentShardedCluster(conn, nil, n, kubeState, log); err != nil {
		return errors.New(fmt.Sprintf("Failed to update OpsManager automation config: %s", err))
	}
	log.Infow("Ops Manager deployment updated successfully")

	return nil
}

func (c *MongoDbController) buildKubeObjectsForShardedCluster(s *mongodb.MongoDbShardedCluster, agentKeySecretName string,
	log *zap.SugaredLogger) (*KubeState, error) {
	spec := s.Spec

	podVars, err := c.buildPodVars(s.Namespace, spec.Project, spec.Credentials, agentKeySecretName)
	if err != nil {
		return nil, err
	}

	// note, that mongos statefulset doesn't have state so no PersistentVolumeClaim is created
	mongosBuilder := c.kubeHelper.NewStatefulSetHelper(s).
		SetName(s.MongosRsName()).
		SetService(s.MongosServiceName()).
		SetReplicas(s.Spec.MongosCount).
		SetPodSpec(NewDefaultPodSpecWrapper(s.Spec.MongosPodSpec)).
		SetPodVars(podVars).
		SetLogger(log).
		SetExposedExternally(true)

	mongosSet := mongosBuilder.BuildStatefulSet()

	err = mongosBuilder.CreateOrUpdateInKubernetes()
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Failed to create Mongos Stateful Set: %s", err))
	}

	log.Infow("Created StatefulSet for mongos servers", "name", mongosSet.Name, "servers count", mongosSet.Spec.Replicas)

	defaultConfigSrvSpec := NewDefaultPodSpec()
	defaultConfigSrvSpec.Storage = DefaultConfigSrvStorageSize
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: spec.ConfigSrvPodSpec,
		Default:        defaultConfigSrvSpec,
	}
	configBuilder := c.kubeHelper.NewStatefulSetHelper(s).
		SetName(s.MongosRsName()).
		SetService(s.ConfigSrvServiceName()).
		SetReplicas(s.Spec.ConfigServerCount).
		SetPersistence(s.Spec.Persistent).
		SetPodSpec(podSpec).
		SetPodVars(podVars).
		SetLogger(log).
		SetExposedExternally(false)

	configSrvSet := configBuilder.BuildStatefulSet()
	configBuilder.CreateOrUpdateInKubernetes()
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Failed to create Config Server Stateful Set: %s", err))
	}

	log.Infow("Created StatefulSet for config servers", "name", configSrvSet.Name, "servers count", configSrvSet.Spec.Replicas)

	shardsSets := make([]*appsv1.StatefulSet, s.Spec.ShardCount)
	shardsNames := make([]string, s.Spec.ShardCount)

	for i := 0; i < s.Spec.ShardCount; i++ {
		shardBuilder := c.kubeHelper.NewStatefulSetHelper(s).
			SetName(s.ShardRsName(i)).
			SetService(s.ShardServiceName()).
			SetReplicas(s.Spec.MongodsPerShardCount).
			SetPersistence(s.Spec.Persistent).
			SetPodSpec(NewDefaultPodSpecWrapper(spec.ShardPodSpec)).
			SetPodVars(podVars).
			SetLogger(log)
		shard := shardBuilder.BuildStatefulSet()
		shardBuilder.CreateOrUpdateInKubernetes()
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Failed to create Stateful Set for shard %s: %s", s.ShardRsName(i), err))
		}

		shardsSets[i] = shard
	}
	log.Infow("Created StatefulSets for shards", "shards", shardsNames)
	return &KubeState{mongosSet: mongosSet, configSrvSet: configSrvSet, shardsSets: shardsSets}, nil
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (c *MongoDbController) updateOmDeploymentShardedCluster(omConnection *om.OmConnection, old,
	new *mongodb.MongoDbShardedCluster, state *KubeState, log *zap.SugaredLogger) error {
	err := waitForAgentsToRegister(new, state, omConnection, log)
	if err != nil {
		return err
	}

	deployment, err := omConnection.ReadDeployment()
	if err != nil {
		return err
	}

	mongosProcesses := createProcesses(state.mongosSet, new.Spec.ClusterName, new.Spec.Version, om.ProcessTypeMongos)
	configRs := buildReplicaSetFromStatefulSet(state.configSrvSet, new.Spec.ClusterName, new.Spec.Version)
	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSets))
	for i, s := range state.shardsSets {
		shards[i] = buildReplicaSetFromStatefulSet(s, new.Spec.ClusterName, new.Spec.Version)
	}

	if err := deployment.MergeShardedCluster(new.Name, mongosProcesses, configRs, shards); err != nil {
		return err
	}
	deployment.AddMonitoringAndBackup(mongosProcesses[0].HostName(), log)

	_, err = omConnection.UpdateDeployment(deployment)
	if err != nil {
		return err
	}

	return nil
}

func (c *MongoDbController) onDeleteShardedCluster(obj interface{}) {
	sc := obj.(*mongodb.MongoDbShardedCluster)
	log := zap.S().With("sharded cluster", sc.Name)

	hostsToRemove := getAllHosts(sc)

	conn, err := c.getOmConnection(sc.Namespace, sc.Spec.Project, sc.Spec.Credentials)
	if err != nil {
		log.Error(err)
		return
	}

	deployment, err := conn.ReadDeployment()
	if err != nil {
		log.Errorf("Failed to read deployment: %s", err)
		return
	}

	if err = deployment.RemoveShardedClusterByName(sc.Name); err != nil {
		log.Errorf("Failed to remove sharded cluster from Ops Manager deployment. %s", err)
		return
	}

	_, err = conn.UpdateDeployment(deployment)
	if err != nil {
		log.Errorf("Failed to push updated deployment to Ops Manager: %s", err)
		return
	}

	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err := om.StopMonitoring(conn, hostsToRemove); err != nil {
		log.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
		return
	}

	log.Info("Removed!")
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

func getAllHosts(c *mongodb.MongoDbShardedCluster) []string {
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

func waitForAgentsToRegister(cluster *mongodb.MongoDbShardedCluster, state *KubeState, omConnection *om.OmConnection,
	log *zap.SugaredLogger) error {
	if err := waitForRsAgentsToRegister(state.mongosSet, cluster.Spec.ClusterName, omConnection, log); err != nil {
		return err
	}
	if err := waitForRsAgentsToRegister(state.configSrvSet, cluster.Spec.ClusterName, omConnection, log); err != nil {
		return err
	}

	for _, s := range state.shardsSets {
		if err := waitForRsAgentsToRegister(s, cluster.Spec.ClusterName, omConnection, log); err != nil {
			return err
		}
	}
	return nil
}
