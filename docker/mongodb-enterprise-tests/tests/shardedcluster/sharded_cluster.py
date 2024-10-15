import kubernetes
import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import run_periodically, skip_if_local
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ShardedClusterTester
from kubetester.operator import Operator
from pytest import fixture, mark
from tests import test_logger

SHARDED_CLUSTER_NAME = "sh001-base"
logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("sharded-cluster.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    return resource.update()


@mark.e2e_sharded_cluster
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_sharded_cluster
class TestShardedClusterCreation(KubernetesTester):
    def test_create_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running)

    def test_sharded_cluster_sts(self, sc: MongoDB):
        sts0 = self.appsv1.read_namespaced_stateful_set(f"{SHARDED_CLUSTER_NAME}-0", sc.namespace)
        assert sts0

    def test_config_sts(self, sc: MongoDB):
        config = self.appsv1.read_namespaced_stateful_set(f"{SHARDED_CLUSTER_NAME}-config", sc.namespace)
        assert config

    def test_mongos_sts(self, sc: MongoDB):
        mongos = self.appsv1.read_namespaced_stateful_set(f"{SHARDED_CLUSTER_NAME}-mongos", sc.namespace)
        assert mongos

    def test_mongod_sharded_cluster_service(self, sc: MongoDB):
        svc0 = self.corev1.read_namespaced_service(f"{SHARDED_CLUSTER_NAME}-sh", sc.namespace)
        assert svc0

    def test_shard0_was_configured(self):
        ShardedClusterTester(SHARDED_CLUSTER_NAME, 1).assert_connectivity()

    @skip_if_local()
    def test_shard0_was_configured_with_srv(self):
        ShardedClusterTester(SHARDED_CLUSTER_NAME, 1, ssl=False, srv=True).assert_connectivity()

    def test_monitoring_versions(self, sc: MongoDB):
        """Verifies that monitoring agent is configured for each process in the deployment"""
        config = self.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 8

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])

    def test_backup_versions(self, sc: MongoDB):
        """Verifies that backup agent is configured for each process in the deployment"""
        config = self.get_automation_config()
        mv = config["backupVersions"]
        assert len(mv) == 8

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])


@mark.e2e_sharded_cluster
class TestShardedClusterUpdate(KubernetesTester):
    def test_scale_up_sharded_cluster(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 2
        sc.update()

        sc.assert_reaches_phase(Phase.Running)

    @skip_if_local()
    def test_shard1_was_configured(self, sc: MongoDB):
        hosts = [
            f"{SHARDED_CLUSTER_NAME}-1-{i}.{SHARDED_CLUSTER_NAME}-sh.{sc.namespace}.svc.cluster.local:27017"
            for i in range(3)
        ]
        logger.debug(f"Checking for connectivity of hosts: {hosts}")
        primary, secondaries = self.wait_for_rs_is_ready(hosts)
        assert primary is not None
        assert len(secondaries) == 2

    def test_monitoring_versions(self, sc: MongoDB):
        """Verifies that monitoring agent is configured for each process in the deployment"""
        config = self.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 11

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])

    def test_backup_versions(self, sc: MongoDB):
        """Verifies that backup agent is configured for each process in the deployment"""
        config = self.get_automation_config()
        mv = config["backupVersions"]
        assert len(mv) == 11

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])


@mark.e2e_sharded_cluster
class TestShardedClusterDeletion(KubernetesTester):
    def test_delete_sharded_cluster_resource(self, sc: MongoDB):
        sc.delete()

        def resource_is_deleted() -> bool:
            try:
                sc.load()
                return False
            except kubernetes.client.ApiException as e:
                return e.status == 404

        run_periodically(resource_is_deleted, timeout=240)

    def test_sharded_cluster_doesnt_exist(self, sc: MongoDB):
        # There should be no statefulsets in this namespace
        sts = self.appsv1.list_namespaced_stateful_set(sc.namespace)
        assert len(sts.items) == 0

    def test_service_does_not_exist(self, sc: MongoDB):
        with pytest.raises(kubernetes.client.ApiException):
            self.corev1.read_namespaced_service(f"{SHARDED_CLUSTER_NAME}-sh", sc.namespace)
