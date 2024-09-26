import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ShardedClusterTester
from tests import test_logger

SHARDED_CLUSTER_NAME = "sh001-base"
logger = test_logger.get_test_logger(__name__)


@pytest.mark.e2e_sharded_cluster
class TestShardedClusterCreation(KubernetesTester):
    """
    name: Sharded Cluster Base Creation
    description: |
      Creates a simple Sharded Cluster with 1 shard, 2 mongos,
      1 replica set as config server and NO persistent volumes.
    create:
      file: sharded-cluster.yaml
      wait_until: in_running_state
      timeout: 360
    """

    def test_sharded_cluster_sts(self):
        sts0 = self.appsv1.read_namespaced_stateful_set(f"{SHARDED_CLUSTER_NAME}-0", self.namespace)
        assert sts0

    def test_config_sts(self):
        config = self.appsv1.read_namespaced_stateful_set(f"{SHARDED_CLUSTER_NAME}-config", self.namespace)
        assert config

    def test_mongos_sts(self):
        mongos = self.appsv1.read_namespaced_stateful_set(f"{SHARDED_CLUSTER_NAME}-mongos", self.namespace)
        assert mongos

    def test_mongod_sharded_cluster_service(self):
        svc0 = self.corev1.read_namespaced_service(f"{SHARDED_CLUSTER_NAME}-sh", self.namespace)
        assert svc0

    def test_shard0_was_configured(self):
        ShardedClusterTester(SHARDED_CLUSTER_NAME, 1).assert_connectivity()

    @skip_if_local()
    def test_shard0_was_configured_with_srv(self):
        ShardedClusterTester(SHARDED_CLUSTER_NAME, 1, ssl=False, srv=True).assert_connectivity()

    def test_monitoring_versions(self):
        """Verifies that monitoring agent is configured for each process in the deployment"""
        config = self.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 8

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])

    def test_backup_versions(self):
        """Verifies that backup agent is configured for each process in the deployment"""
        config = self.get_automation_config()
        mv = config["backupVersions"]
        assert len(mv) == 8

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])


@pytest.mark.e2e_sharded_cluster
class TestShardedClusterUpdate(KubernetesTester):
    """
    name: Sharded Cluster Base Creation
    description: |
      Scales a Sharded Cluster from 1 to 2 Shards
    update:
      file: sharded-cluster.yaml
      patch: '[{"op":"replace","path":"/spec/shardCount","value":2}]'
      wait_until: in_running_state
      timeout: 360
    """

    @skip_if_local()
    def test_shard1_was_configured(self):
        hosts = [
            f"{SHARDED_CLUSTER_NAME}-1-{i}.{SHARDED_CLUSTER_NAME}-sh.{self.namespace}.svc.cluster.local:27017"
            for i in range(3)
        ]
        logger.debug(f"Checking for connectivity of hosts: {hosts}")
        primary, secondaries = self.wait_for_rs_is_ready(hosts)
        assert primary is not None
        assert len(secondaries) == 2

    def test_monitoring_versions(self):
        """Verifies that monitoring agent is configured for each process in the deployment"""
        config = self.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 11

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])

    def test_backup_versions(self):
        """Verifies that backup agent is configured for each process in the deployment"""
        config = self.get_automation_config()
        mv = config["backupVersions"]
        assert len(mv) == 11

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])


@pytest.mark.e2e_sharded_cluster
class TestShardedClusterDeletion(KubernetesTester):
    """
    name: Sharded Cluster Base Deletion
    description: |
      Removes a Sharded Cluster
    delete:
      file: sharded-cluster.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_sharded_cluster_doesnt_exist(self):
        # There should be no statefulsets in this namespace
        sts = self.appsv1.list_namespaced_stateful_set(self.namespace)
        assert len(sts.items) == 0

    def test_service_does_not_exist(self):
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service(f"{SHARDED_CLUSTER_NAME}-sh", self.namespace)
