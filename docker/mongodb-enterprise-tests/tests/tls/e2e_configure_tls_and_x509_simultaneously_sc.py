import pytest
from kubetester.certs import (
    ISSUER_CA_NAME,
    Certificate,
    create_agent_tls_certs,
    create_mongodb_tls_certs,
    create_sharded_cluster_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ShardedClusterTester
from kubetester.omtester import get_sc_cert_names

MDB_RESOURCE = "sh001-base"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str) -> str:
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE,
        shards=1,
        mongos_per_shard=3,
        config_servers=3,
        mongos=2,
    )


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("sharded-cluster.yaml"), namespace=namespace)
    res["spec"]["security"] = {"tls": {"ca": issuer_ca_configmap}}
    return res.create()


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
def test_standalone_running(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
def test_connectivity():
    tester = ShardedClusterTester(MDB_RESOURCE, 2)
    tester.assert_connectivity()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
def test_enable_x509(mdb: MongoDB, agent_certs: str):
    mdb.load()
    mdb["spec"]["security"] = {
        "authentication": {"enabled": True},
        "modes": ["X509"],
    }

    mdb.assert_reaches_phase(Phase.Running, timeout=400)
