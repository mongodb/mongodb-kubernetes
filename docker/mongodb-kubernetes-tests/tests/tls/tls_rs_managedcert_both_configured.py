import pytest
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import (
    CA_ISSUER_REF,
    assert_leaf_secrets_created,
    managed_cert_replica_set,
    read_ca_bundles,
    scale_and_assert_member_cert_issuance,
)

MDB_RESOURCE = "test-tls-rs-managedcert-both-configured"


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str, custom_mdb_version: str) -> MongoDB:
    # Both categories use the user configured issuer, so the operator sets up no self-signed CA.
    return managed_cert_replica_set(
        MDB_RESOURCE, namespace, custom_mdb_version, {"server": CA_ISSUER_REF, "client": CA_ISSUER_REF}
    )


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_operator_created_certs_from_one_issuer(mdb: MongoDB, namespace: str):
    assert_leaf_secrets_created(MDB_RESOURCE, namespace)

    # The operator builds both CA bundles from the ca.crt that cert-manager writes into each issued
    # leaf secret. One issuer signs both categories here, so both bundles hold the same CA.
    server_ca, client_ca = read_ca_bundles(MDB_RESOURCE, namespace)
    assert server_ca == client_ca, "one issuer signs both categories, so the server and client CA bundles should match"


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
@skip_if_local()
def test_mdb_is_not_reachable_without_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
@skip_if_local()
def test_mdb_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    # The server certs are signed by ca-issuer, so a client trusting ca-issuer's CA (ca_path) can
    # verify the connection.
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_scale_up(mdb: MongoDB, namespace: str):
    scale_and_assert_member_cert_issuance(mdb, MDB_RESOURCE, namespace, 5, reissued=True)


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
@skip_if_local()
def test_reachable_with_ssl_after_scale_up(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_scale_down(mdb: MongoDB, namespace: str):
    scale_and_assert_member_cert_issuance(mdb, MDB_RESOURCE, namespace, 3, reissued=False)
