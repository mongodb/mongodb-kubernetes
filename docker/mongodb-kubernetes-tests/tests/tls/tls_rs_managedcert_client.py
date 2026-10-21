import pytest
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import (
    CA_ISSUER_REF,
    assert_leaf_certs_signed_by_its_category_ca,
    assert_leaf_secrets_created,
    managed_cert_replica_set,
    managed_certificate_operator,
    read_ca_bundles,
    scale_and_assert_member_cert_issuance,
)

MDB_RESOURCE = "test-tls-rs-managedcert-client"


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str, custom_mdb_version: str) -> MongoDB:
    # The server category uses the user configured issuer (ca-issuer). The client category has no
    # issuer, so the operator self-signs the client CA.
    return managed_cert_replica_set(MDB_RESOURCE, namespace, custom_mdb_version, {"server": CA_ISSUER_REF})


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_operator_created_all_cert_secrets_and_ca(mdb: MongoDB, namespace: str):
    assert_leaf_secrets_created(MDB_RESOURCE, namespace)

    # The server CA comes from the user configured issuer, the client CA is self-signed by the
    # operator, so they differ and each leaf cert must be signed by its own category's CA.
    server_ca, client_ca = read_ca_bundles(MDB_RESOURCE, namespace)
    assert server_ca != client_ca, "server CA and client CA should differ (server=issuer, client=self-signed)"
    assert_leaf_certs_signed_by_its_category_ca(MDB_RESOURCE, namespace, server_ca, client_ca)


@pytest.mark.e2e_replica_set_tls_managedcert_client
@skip_if_local()
def test_mdb_is_not_reachable_without_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_managedcert_client
@skip_if_local()
def test_mdb_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_scale_up(mdb: MongoDB, namespace: str):
    scale_and_assert_member_cert_issuance(mdb, MDB_RESOURCE, namespace, 5, reissued=True)


@pytest.mark.e2e_replica_set_tls_managedcert_client
@skip_if_local()
def test_reachable_with_ssl_after_scale_up(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_scale_down(mdb: MongoDB, namespace: str):
    scale_and_assert_member_cert_issuance(mdb, MDB_RESOURCE, namespace, 3, reissued=False)
