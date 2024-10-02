import pytest
from kubetester import find_fixture
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    Certificate,
    create_sharded_cluster_certs,
    create_x509_agent_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture

MDB_RESOURCE_NAME = "test-x509-all-options-sc"


@fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=1,
        mongos_per_shard=3,
        config_servers=3,
        mongos=2,
        internal_auth=True,
        x509_certs=True,
    )


@fixture(scope="module")
def sharded_cluster(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("test-x509-all-options-sc.yaml"),
        namespace=namespace,
    )
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    yield resource.update()


@pytest.mark.e2e_tls_x509_configure_all_options_sc
class TestShardedClusterEnableAllOptions(KubernetesTester):
    def test_gets_to_running_state(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated(self):
        ac_tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        ac_tester.assert_internal_cluster_authentication_enabled()
        ac_tester.assert_authentication_enabled()
        ac_tester.assert_expected_users(0)

    def test_rotate_shard_certfile(self, sharded_cluster: MongoDB, namespace: str):
        assert_certificate_rotation(sharded_cluster, namespace, "{}-0-clusterfile".format(MDB_RESOURCE_NAME))

    def test_rotate_config_certfile(self, sharded_cluster: MongoDB, namespace: str):
        assert_certificate_rotation(
            sharded_cluster,
            namespace,
            "{}-config-clusterfile".format(MDB_RESOURCE_NAME),
        )

    def test_rotate_mongos_certfile(self, sharded_cluster: MongoDB, namespace: str):
        assert_certificate_rotation(
            sharded_cluster,
            namespace,
            "{}-mongos-clusterfile".format(MDB_RESOURCE_NAME),
        )


def assert_certificate_rotation(sharded_cluster, namespace, certificate_name):
    cert = Certificate(name=certificate_name, namespace=namespace)
    cert.load()
    cert["spec"]["dnsNames"].append("foo")  # Append DNS to cert to rotate the certificate
    cert.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)
