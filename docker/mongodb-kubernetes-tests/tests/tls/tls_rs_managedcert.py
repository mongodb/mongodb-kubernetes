import pytest
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import (
    assert_leaf_certs_signed_by_its_category_ca,
    assert_leaf_secrets_created,
    assert_self_signed_ca_created,
    managed_cert_replica_set,
    member_cert_issuance,
    read_ca_bundles,
    scale_and_wait_for_running,
)

MDB_RESOURCE = "test-tls-rs-managedcert"


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str, custom_mdb_version: str) -> MongoDB:
    # No issuer for either category: the operator self-signs both the server and the client CAs.
    return managed_cert_replica_set(MDB_RESOURCE, namespace, custom_mdb_version, {})


@pytest.mark.e2e_replica_set_tls_managedcert
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert
def test_replica_set_running(mdb: MongoDB):
    # Reaching Running is itself the functional proof that fully self-signed mTLS works
    # end-to-end, members validate each other's clusterfile (client self-signed CA) via
    # clusterCAFile and each other's server cert (server self-signed CA) via CAFile, and
    # the agent authenticates with no user configured CA anywhere.
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert
def test_operator_created_certs_and_both_self_signed_cas(mdb: MongoDB, namespace: str):
    assert_leaf_secrets_created(MDB_RESOURCE, namespace)

    # Because no issuer is configured, the operator provisioned both per-category self-signed
    # root CAs (server signs the member cert, client signs clusterfile/agent).
    assert_self_signed_ca_created(MDB_RESOURCE, namespace, "server")
    assert_self_signed_ca_created(MDB_RESOURCE, namespace, "client")

    server_ca, client_ca = read_ca_bundles(MDB_RESOURCE, namespace)
    assert server_ca != client_ca, "server and client self-signed CAs should be distinct roots"
    assert_leaf_certs_signed_by_its_category_ca(MDB_RESOURCE, namespace, server_ca, client_ca)


@pytest.mark.e2e_replica_set_tls_managedcert
@skip_if_local()
def test_mdb_is_not_reachable_without_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_managedcert
def test_scale_up_issues_the_cert_once(mdb: MongoDB, namespace: str):
    revision_before, cert_before = member_cert_issuance(MDB_RESOURCE, namespace)

    scale_and_wait_for_running(mdb, 5)

    # The operator adds members one at a time, but the cert covers the target (5) from the first
    # step, so the whole scale up issues it exactly once instead of once per member.
    revision_after, cert_after = member_cert_issuance(MDB_RESOURCE, namespace)
    assert revision_after == revision_before + 1, "member cert must be issued once for the whole scale up"
    assert cert_after != cert_before, "member cert must be reissued to cover the new members"


@pytest.mark.e2e_replica_set_tls_managedcert
def test_scale_down_needs_no_new_certs(mdb: MongoDB, namespace: str):
    revision_before, cert_before = member_cert_issuance(MDB_RESOURCE, namespace)

    scale_and_wait_for_running(mdb, 3)

    # The operator never shrinks the member cert SANs, so a scale down must not issue the cert
    # again: cert-manager's revision is unchanged and the content is byte-for-byte the same.
    revision_after, cert_after = member_cert_issuance(MDB_RESOURCE, namespace)
    assert revision_after == revision_before, "member cert must not be reissued on scale down"
    assert cert_after == cert_before, "member cert must not change on scale down"


@pytest.mark.e2e_replica_set_tls_managedcert
def test_scale_up_within_peak_does_not_reissue(mdb: MongoDB, namespace: str):
    revision_before, cert_before = member_cert_issuance(MDB_RESOURCE, namespace)

    scale_and_wait_for_running(mdb, 4)

    # The cluster peaked at 5 members earlier, then scaled down to 3. The cert still covers members
    # 0-4, so scaling back up to 4 reuses a name already in the cert and must not reissue it. No
    # reissue means no trigger to restart or roll the members.
    revision_after, cert_after = member_cert_issuance(MDB_RESOURCE, namespace)
    assert revision_after == revision_before, "member cert must not be reissued on a scale up within the SAN peak"
    assert cert_after == cert_before, "member cert must not change on a scale up within the SAN peak"
