import pytest
from kubetester import try_load
from kubetester.certs import ISSUER_CA_NAME, Certificate, create_mongodb_tls_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE = "test-tls-base-rs-require-ssl"
MDB_RESOURCE_CUSTOM_CA = "test-tls-rs-custom-ca-path"
CUSTOM_CA_FILE_PATH = "/var/lib/tls/custom-ca/ca-pem"


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
    resource = MongoDB.from_yaml(load_fixture("test-tls-base-rs-require-ssl.yaml"), namespace=namespace)
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_require
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_replica_set_tls_require
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_is_not_reachable_without_ssl(mdb: MongoDB):
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
def test_mdb_scaled_up_is_not_reachable_without_ssl(mdb: MongoDB):
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
def test_mdb_scaled_down_is_not_reachable_without_ssl(mdb: MongoDB):
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
def test_mdb_renewed_is_not_reachable_without_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require
@skip_if_local()
def test_mdb_renewed_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


# --- custom caFilePath tests ---


@pytest.fixture(scope="module")
def server_certs_custom_ca(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MDB_RESOURCE_CUSTOM_CA,
        f"{MDB_RESOURCE_CUSTOM_CA}-cert",
        replicas=3,
    )


@pytest.fixture(scope="module")
def mdb_custom_ca(namespace: str, server_certs_custom_ca: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        load_fixture("test-tls-base-rs-require-ssl.yaml"),
        name=MDB_RESOURCE_CUSTOM_CA,
        namespace=namespace,
    )
    resource.set_architecture_annotation()
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    resource["spec"]["security"]["tls"]["caFilePath"] = CUSTOM_CA_FILE_PATH
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_require_custom_ca_path
def test_custom_ca_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_replica_set_tls_require_custom_ca_path
def test_custom_ca_replica_set_running(mdb_custom_ca: MongoDB):
    mdb_custom_ca.update()
    mdb_custom_ca.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_tls_require_custom_ca_path
@skip_if_local()
def test_custom_ca_mdb_is_not_reachable_without_ssl(mdb_custom_ca: MongoDB):
    mdb_custom_ca.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require_custom_ca_path
@skip_if_local()
def test_custom_ca_mdb_is_reachable_with_ssl(mdb_custom_ca: MongoDB, ca_path: str):
    mdb_custom_ca.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_custom_ca_path
def test_custom_ca_path_in_automation_config(mdb_custom_ca: MongoDB):
    ac = mdb_custom_ca.get_automation_config_tester().automation_config
    assert ac.get("tls", {}).get("CAFilePath") == CUSTOM_CA_FILE_PATH, (
        f"Expected AC tls.CAFilePath={CUSTOM_CA_FILE_PATH}, "
        f"got {ac.get('tls', {}).get('CAFilePath')}"
    )
