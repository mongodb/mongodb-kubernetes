from kubetester.certs import SetProperties, create_mongodb_tls_certs
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark

MDB_RESOURCE = "my-replica-set"
SUBJECT = {"organizations": ["MDB Tests"], "organizationalUnits": ["Servers"]}
SERVER_SET = SetProperties(MDB_RESOURCE, MDB_RESOURCE + "-svc", 3)


@fixture(scope="module")
def all_certs(issuer, namespace) -> None:
    """Generates TLS Certificates for the servers."""
    spec_server = {
        "subject": SUBJECT,
        "usages": ["server auth"],
    }

    spec_client = {
        "subject": SUBJECT,
        "usages": ["client auth"],
    }

    server_set = SERVER_SET
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
def replica_set(
    namespace: str,
    all_certs,
    issuer_ca_configmap: str,
) -> MongoDB:
    _ = all_certs
    mdb: MongoDB = MongoDB.from_yaml(
        _fixture("replica-set-scram-sha-256-x509-internal-cluster.yaml"),
        namespace=namespace,
    )
    mdb["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return mdb.create()


@mark.e2e_replica_set_scram_x509_ic_manual_certs
def test_create_replica_set_with_x509_internal_cluster(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_replica_set_scram_x509_ic_manual_certs
def test_create_replica_can_connect(replica_set: MongoDB, ca_path: str):
    # The ca_path fixture indicates a relative path in the testing Pod to the
    # CA file we can use to validate against the certificates generated
    # by cert-manager.
    replica_set.assert_connectivity(ca_path=ca_path)


@mark.e2e_replica_set_scram_x509_ic_manual_certs
def test_ops_manager_state_was_updated_correctly(replica_set: MongoDB):
    ac_tester = replica_set.get_automation_config_tester()
    ac_tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)
    ac_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
    ac_tester.assert_expected_users(0)
    ac_tester.assert_authoritative_set(True)
    ac_tester.assert_internal_cluster_authentication_enabled()
