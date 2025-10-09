import pytest
from kubetester.certs import ISSUER_CA_NAME, Certificate, create_mongodb_tls_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE = "test-tls-base-rs-require-ssl"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MDB_RESOURCE,
        f"{MDB_RESOURCE}-cert",
        replicas=5,
    )


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-tls-base-rs-require-ssl.yaml"), namespace=namespace)

    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_replica_set_tls_require
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_replica_set_tls_require
def test_replica_set_running(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_is_reachable_with_no_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require
def test_scale_up_replica_set(mdb: MongoDB):
    mdb.load()
    mdb["spec"]["members"] = 5
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_scaled_up_is_not_reachable_with_no_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_scaled_up_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require
def test_scale_down_replica_set(mdb: MongoDB):
    mdb.load()
    mdb["spec"]["members"] = 3
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1000)


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_scaled_down_is_reachable_with_no_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_scaled_down_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require
def test_change_certificate_and_wait_for_running(mdb: MongoDB, namespace: str):
    """
    Perform certificate rotation by updating dns in certs
    """
    cert = Certificate(name=f"{MDB_RESOURCE}-cert", namespace=namespace).load()
    cert["spec"]["dnsNames"].append("foo")
    cert.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_renewed_is_reachable_with_no_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_renewed_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()
