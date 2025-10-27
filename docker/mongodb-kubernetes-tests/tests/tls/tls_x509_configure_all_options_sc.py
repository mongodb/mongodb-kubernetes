import pytest
from kubetester import find_fixture, try_load
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    create_sharded_cluster_certs,
    create_x509_agent_tls_certs,
    rotate_and_assert_certificates,
)
from kubetester.kubetester import KubernetesTester, is_multi_cluster
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger
from tests.shardedcluster.conftest import enable_multi_cluster_deployment

MDB_RESOURCE_NAME = "test-x509-all-options-sc"

logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    shard_distribution = None
    mongos_distribution = None
    config_srv_distribution = None
    if is_multi_cluster():
        shard_distribution = [1, 1, 1]
        mongos_distribution = [1, 1, 1]
        config_srv_distribution = [1, 1, 1]

    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=1,
        mongod_per_shard=3,
        config_servers=3,
        mongos=3,
        internal_auth=True,
        x509_certs=True,
        shard_distribution=shard_distribution,
        mongos_distribution=mongos_distribution,
        config_srv_distribution=config_srv_distribution,
    )


@fixture(scope="module")
def sc(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("test-x509-all-options-sc.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, 1],
            configsrv_members_array=[1, 1, 1],
        )

    return resource.update()


@pytest.mark.e2e_tls_x509_configure_all_options_sc
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_x509_configure_all_options_sc
class TestShardedClusterEnableAllOptions:

    def test_gets_to_running_state(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_ops_manager_state_correctly_updated(self):
        ac_tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        ac_tester.assert_internal_cluster_authentication_enabled()
        ac_tester.assert_authentication_enabled()
        ac_tester.assert_expected_users(0)

    def test_rotate_shard_cert(self, sc: MongoDB, namespace: str):
        rotate_and_assert_certificates(sc, namespace, f"{MDB_RESOURCE_NAME}-0-cert")

    def test_rotate_config_cert(self, sc: MongoDB, namespace: str):
        rotate_and_assert_certificates(sc, namespace, f"{MDB_RESOURCE_NAME}-config-cert")

    def test_rotate_mongos_cert(self, sc: MongoDB, namespace: str):
        rotate_and_assert_certificates(sc, namespace, f"{MDB_RESOURCE_NAME}-mongos-cert")

    def test_rotate_agent_certificate(self, sc: MongoDB, namespace: str):
        rotate_and_assert_certificates(sc, namespace, "agent-certs")
