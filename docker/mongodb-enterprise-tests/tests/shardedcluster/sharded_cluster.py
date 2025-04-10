import kubernetes
from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import run_periodically, skip_if_local, skip_if_multi_cluster
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import is_multi_cluster
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
    get_mongos_service_names,
)

SCALED_SHARD_COUNT = 2
logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("sharded-cluster.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, None],
            configsrv_members_array=[1, 1, 1],
        )

    return resource


@mark.e2e_sharded_cluster
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster
class TestShardedClusterCreation:
    def test_create_sharded_cluster(self, sc: MongoDB):
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=800)

    def test_sharded_cluster_sts(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            if sc.shard_members_in_cluster(cluster_member_client.cluster_name) > 0:
                # Single shard exists in this test case
                sts_name = sc.shard_statefulset_name(0, cluster_member_client.cluster_index)
                sts = cluster_member_client.read_namespaced_stateful_set(sts_name, sc.namespace)
                assert sts

    def test_config_srv_sts(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            if sc.config_srv_members_in_cluster(cluster_member_client.cluster_name) > 0:
                sts_name = sc.config_srv_statefulset_name(cluster_member_client.cluster_index)
                sts = cluster_member_client.read_namespaced_stateful_set(sts_name, sc.namespace)
                assert sts

    def test_mongos_sts(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            if sc.mongos_members_in_cluster(cluster_member_client.cluster_name) > 0:
                sts_name = sc.mongos_statefulset_name(cluster_member_client.cluster_index)
                sts = cluster_member_client.read_namespaced_stateful_set(sts_name, sc.namespace)
                assert sts

    def test_mongod_sharded_cluster_service(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            if sc.shard_members_in_cluster(cluster_member_client.cluster_name) > 0:
                svc_name = sc.shard_service_name()
                svc = cluster_member_client.read_namespaced_service(svc_name, sc.namespace)
                assert svc

    # When testing locally make sure you have kubefwd forwarding all cluster hostnames
    # kubefwd does not contain fix for multiple cluster, use https://github.com/lsierant/kubefwd fork instead
    def test_shards_were_configured_and_accessible(self, sc: MongoDB):
        for service_name in get_mongos_service_names(sc):
            tester = sc.tester(service_names=[service_name])
            tester.assert_connectivity()

    @skip_if_local()  # Local machine DNS don't contain K8s CoreDNS SRV records which are required
    @skip_if_multi_cluster()  # srv option does not work for multi-cluster tests as each cluster DNS contains entries
    # related only to that cluster. Additionally, we don't pass srv option when building multi-cluster conn string
    def test_shards_were_configured_with_srv_and_accessible(self, sc: MongoDB):
        for service_name in get_mongos_service_names(sc):
            tester = sc.tester(service_names=[service_name], srv=True)
            tester.assert_connectivity()

    def test_monitoring_versions(self, sc: MongoDB):
        """Verifies that monitoring agent is configured for each process in the deployment"""
        config = KubernetesTester.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 8

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])

    def test_backup_versions(self, sc: MongoDB):
        """Verifies that backup agent is configured for each process in the deployment"""
        config = KubernetesTester.get_automation_config()
        mv = config["backupVersions"]
        assert len(mv) == 8

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])


@mark.e2e_sharded_cluster
class TestShardedClusterUpdate:
    def test_scale_up_sharded_cluster(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = SCALED_SHARD_COUNT
        sc.update()

        sc.assert_reaches_phase(Phase.Running)

    def test_both_shards_are_configured(self, sc: MongoDB):
        for shard_idx in range(SCALED_SHARD_COUNT):
            hosts = []
            for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
                for member_idx in range(sc.shard_members_in_cluster(cluster_member_client.cluster_name)):
                    hostname = sc.shard_hostname(shard_idx, member_idx, cluster_member_client.cluster_index)
                    hosts.append(hostname)

            logger.debug(f"Checking for connectivity of hosts: {hosts}")
            primary, secondaries = KubernetesTester.wait_for_rs_is_ready(hosts)
            assert primary is not None
            assert len(secondaries) == 2

    def test_monitoring_versions(self, sc: MongoDB):
        """Verifies that monitoring agent is configured for each process in the deployment"""
        config = KubernetesTester.get_automation_config()
        mv = config["monitoringVersions"]
        assert len(mv) == 11

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])

    def test_backup_versions(self, sc: MongoDB):
        """Verifies that backup agent is configured for each process in the deployment"""
        config = KubernetesTester.get_automation_config()
        mv = config["backupVersions"]
        assert len(mv) == 11

        for process in config["processes"]:
            assert any(agent for agent in mv if agent["hostname"] == process["hostname"])


@mark.e2e_sharded_cluster
class TestShardedClusterDeletion:

    # We need to store cluster_member_clients somehow after deleting the MongoDB resource.
    # Cluster mapping from deployment state is needed to compute cluster_member_clients.
    @fixture(scope="class")
    def cluster_member_clients(self, sc: MongoDB):
        return get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace)

    def test_delete_sharded_cluster_resource(self, sc: MongoDB, cluster_member_clients):
        sc.delete()

        def resource_is_deleted() -> bool:
            try:
                sc.load()
                return False
            except kubernetes.client.ApiException as e:
                return e.status == 404

        run_periodically(resource_is_deleted, timeout=240)

    def test_sharded_cluster_doesnt_exist(self, sc: MongoDB, cluster_member_clients):
        def sts_are_deleted() -> bool:
            for cluster_member_client in cluster_member_clients:
                sts = cluster_member_client.list_namespaced_stateful_sets(sc.namespace)
                if len(sts.items) != 0:
                    return False

            return True

        run_periodically(sts_are_deleted, timeout=60)

    def test_service_does_not_exist(self, sc: MongoDB, cluster_member_clients):
        def svc_are_deleted() -> bool:
            for cluster_member_client in cluster_member_clients:
                try:
                    cluster_member_client.read_namespaced_service(sc.shard_service_name(), sc.namespace)
                    return False
                except kubernetes.client.ApiException as e:
                    if e.status != 404:
                        return False

            return True

        run_periodically(svc_are_deleted, timeout=60)
