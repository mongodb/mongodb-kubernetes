import pytest
from kubetester import try_load
from kubetester.certs import create_agent_tls_certs, create_sharded_cluster_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import is_multi_cluster
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ShardedClusterTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.shardedcluster.conftest import enable_multi_cluster_deployment, get_mongos_service_names

MDB_RESOURCE = "test-ssl-with-x509-sc"
CERT_SECRET_PREFIX = "prefix"


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
        MDB_RESOURCE,
        shards=1,
        mongod_per_shard=3,
        config_servers=3,
        mongos=2,
        secret_prefix=f"{CERT_SECRET_PREFIX}-",
        shard_distribution=shard_distribution,
        mongos_distribution=mongos_distribution,
        config_srv_distribution=config_srv_distribution,
    )


@pytest.fixture(scope="module")
def sc(namespace: str, server_certs: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("sharded-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE)

    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, None],
            configsrv_members_array=[1, 1, 1],
        )

    try_load(resource)
    return resource


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE, secret_prefix=CERT_SECRET_PREFIX)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
def test_standalone_running(sc: MongoDB):
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
def test_connectivity_without_ssl(sc: MongoDB):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(use_ssl=False, service_names=service_names)
    tester.assert_connectivity()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
def test_enable_tls(sc: MongoDB, issuer_ca_configmap: str):
    sc["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {"ca": issuer_ca_configmap},
    }
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
def test_connectivity_with_ssl(sc: MongoDB, ca_path: str):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(use_ssl=True, ca_path=ca_path, service_names=service_names)
    tester.assert_connectivity()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
def test_enable_x509(sc: MongoDB, agent_certs: str):
    sc["spec"]["security"]["authentication"] = {
        "agents": {"mode": "X509"},
        "enabled": True,
        "modes": ["X509"],
    }
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)
