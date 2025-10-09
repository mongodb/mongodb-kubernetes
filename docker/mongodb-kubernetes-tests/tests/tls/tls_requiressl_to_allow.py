from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark

MDB_RESOURCE = "test-tls-base-rs-require-ssl"


@fixture(scope="module")
def rs_certs_secret(namespace: str, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, MDB_RESOURCE, "certs-test-tls-base-rs-require-ssl-cert")
    return "certs"


@fixture(scope="module")
def tls_replica_set(
    namespace: str,
    custom_mdb_version: str,
    issuer_ca_configmap: str,
    rs_certs_secret: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("test-tls-base-rs-require-ssl.yaml"),
        name=MDB_RESOURCE,
        namespace=namespace,
    )

    resource.set_version(custom_mdb_version)
    # no TLS to start with
    resource["spec"]["security"] = {}

    yield resource.create()

    resource.delete()


@mark.e2e_replica_set_tls_require_to_allow
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_replica_set_tls_require_to_allow
def test_replica_set_creation(tls_replica_set: MongoDB):
    tls_replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_replica_set_tls_require_to_allow
def test_enable_tls(tls_replica_set: MongoDB, issuer_ca_configmap: str, rs_certs_secret: str):
    tls_replica_set.configure_custom_tls(
        issuer_ca_configmap_name=issuer_ca_configmap,
        tls_cert_secret_name=rs_certs_secret,
    )
    tls_replica_set.update()
    tls_replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_replica_set_tls_require_to_allow
@skip_if_local()
def test_replica_set_is_not_reachable_without_tls(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=False)
    tester.assert_no_connection()


@mark.e2e_replica_set_tls_require_to_allow
@skip_if_local()
def test_replica_set_is_reachable_with_tls(tls_replica_set: MongoDB, ca_path: str):
    tester = tls_replica_set.tester(use_ssl=True, ca_path=ca_path)
    tester.assert_connectivity()


@mark.e2e_replica_set_tls_require_to_allow
def test_configure_allow_ssl(tls_replica_set: MongoDB):
    """
    Change ssl configuration to allowSSL
    """
    tls_replica_set["spec"]["additionalMongodConfig"] = {"net": {"ssl": {"mode": "allowSSL"}}}

    tls_replica_set.update()
    tls_replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_replica_set_tls_require_to_allow
@skip_if_local()
def test_replica_set_is_reachable_without_tls_allow_ssl(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=False)
    tester.assert_connectivity()


@mark.e2e_replica_set_tls_require_to_allow
@skip_if_local()
def test_replica_set_is_reachable_with_tls_allow_ssl(tls_replica_set: MongoDB, ca_path: str):
    tester = tls_replica_set.tester(use_ssl=True, ca_path=ca_path)
    tester.assert_connectivity()
