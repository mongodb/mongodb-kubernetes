from kubetester.certs import SetProperties, create_mongodb_tls_certs
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark

MDB_RESOURCE = "sharded-cluster-scram-sha-256"
SUBJECT = {"organizations": ["MDB Tests"], "organizationalUnits": ["Servers"]}
SERVER_SETS = frozenset(
    [
        SetProperties(MDB_RESOURCE + "-0", MDB_RESOURCE + "-sh", 3),
        SetProperties(MDB_RESOURCE + "-config", MDB_RESOURCE + "-cs", 3),
        SetProperties(MDB_RESOURCE + "-mongos", MDB_RESOURCE + "-svc", 2),
    ]
)


@fixture(scope="module")
def all_certs(issuer, namespace) -> None:
    """Generates all required TLS certificates: Servers and Client/Member."""
    spec_server = {
        "subject": SUBJECT,
        "usages": ["server auth"],
    }
    spec_client = {
        "subject": SUBJECT,
        "usages": ["client auth"],
    }

    for server_set in SERVER_SETS:
        create_mongodb_tls_certs(
            issuer,
            namespace,
            server_set.name,
            server_set.name + "-cert",
            server_set.replicas,
            service_name=server_set.service,
            spec=spec_server,
        )
        create_mongodb_tls_certs(
            issuer,
            namespace,
            server_set.name,
            server_set.name + "-clusterfile",
            server_set.replicas,
            service_name=server_set.service,
            spec=spec_client,
        )


@fixture(scope="module")
def sharded_cluster(
    namespace: str,
    all_certs,
    issuer_ca_configmap: str,
) -> MongoDB:
    mdb: MongoDB = MongoDB.from_yaml(
        _fixture("sharded-cluster-scram-sha-256-x509-internal-cluster.yaml"),
        namespace=namespace,
    )
    mdb["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return mdb.create()


@mark.e2e_sharded_cluster_scram_x509_ic_manual_certs
def test_create_replica_set_with_x509_internal_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_sharded_cluster_scram_x509_ic_manual_certs
def test_create_replica_can_connect(sharded_cluster: MongoDB, ca_path: str):
    sharded_cluster.assert_connectivity(ca_path=ca_path)


@mark.e2e_sharded_cluster_scram_x509_ic_manual_certs
def test_ops_manager_state_was_updated_correctly(sharded_cluster: MongoDB):
    ac_tester = sharded_cluster.get_automation_config_tester()
    ac_tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)
    ac_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
    ac_tester.assert_internal_cluster_authentication_enabled()

    ac_tester.assert_expected_users(0)
    ac_tester.assert_authoritative_set(True)
