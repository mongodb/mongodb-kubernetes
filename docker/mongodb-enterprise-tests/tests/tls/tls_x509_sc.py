import pytest
from kubetester import try_load
from kubetester.certs import create_sharded_cluster_certs, create_x509_agent_tls_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import is_multi_cluster
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from tests.shardedcluster.conftest import enable_multi_cluster_deployment

MDB_RESOURCE_NAME = "test-x509-sc"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    shard_distribution = None
    mongos_distribution = None
    config_srv_distribution = None
    if is_multi_cluster():
        shard_distribution = [1, 1, 1]
        mongos_distribution = [1, 1, None]
        config_srv_distribution = [1, 1, 1]

    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=1,
        mongod_per_shard=3,
        config_servers=3,
        mongos=2,
        shard_distribution=shard_distribution,
        mongos_distribution=mongos_distribution,
        config_srv_distribution=config_srv_distribution,
    )


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@pytest.fixture(scope="module")
def sharded_cluster(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-x509-sc.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, None],
            configsrv_members_array=[1, 1, 1],
        )

    return resource.update()


@pytest.mark.e2e_tls_x509_sc
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_x509_sc
class TestClusterWithTLSCreation:
    def test_sharded_cluster_running(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)
