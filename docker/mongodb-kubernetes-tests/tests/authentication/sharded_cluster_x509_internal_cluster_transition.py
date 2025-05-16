from kubetester import find_fixture
from kubetester.certs import create_sharded_cluster_certs, create_x509_agent_tls_certs
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark

MDB_RESOURCE_NAME = "sc-internal-cluster-auth-transition"


@fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=2,
        mongod_per_shard=3,
        config_servers=1,
        mongos=1,
        internal_auth=True,
        x509_certs=True,
    )


@fixture(scope="module")
def sc(namespace: str, server_certs, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-x509-internal-cluster-auth-transition.yaml"),
        namespace=namespace,
    )
    resource["spec"]["security"] = {
        "tls": {
            "enabled": True,
            "ca": issuer_ca_configmap,
        },
        "authentication": {"enabled": True, "modes": ["X509"]},
    }
    yield resource.update()


@mark.e2e_sharded_cluster_internal_cluster_transition
def test_create_resource(sc: MongoDB):
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_sharded_cluster_internal_cluster_transition
def test_enable_internal_cluster_authentication(sc: MongoDB):
    sc.load()
    sc["spec"]["security"]["authentication"]["internalCluster"] = "X509"
    sc.update()

    sc.assert_reaches_phase(Phase.Running, timeout=2400)
