"""A TLS replica set with operator-managed certificates and keyfile internal cluster auth.

The replica set starts with SCRAM external auth and keyfile internal auth, so the operator issues only
the member certificate: there is no clusterfile and no agent certificate. Without a clusterfile, mongod
also presents the member certificate when it connects to the other members, so that certificate is used
as a client certificate too and carries dual (clientAuth, serverAuth) key usage.

Switching internal cluster auth to x509 is not supported while managed certificates are enabled: the
transition goes through a mixed state where old and restarted members validate each other against
different CAs, so the deployment never converges. The tests assert the operator rejects the switch and
that the member certificate carries the membership subject.
"""

import pytest
from kubernetes.client.rest import ApiException
from kubetester import read_secret, try_load
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import (
    assert_member_certificate_client_usage,
    leaf_secret_names,
    managed_certificate_operator,
    subject_from_certificate,
)

MDB_RESOURCE = "test-tls-rs-managedcert-scram"


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-tls-base-rs.yaml"), name=MDB_RESOURCE, namespace=namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["security"]["authentication"] = {"enabled": True, "modes": ["SCRAM"]}
    # No issuer, so the operator self-signs the member certificate with its own server CA.
    resource["spec"]["security"]["managedCertificate"] = {}
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_managedcert_internal_auth_keyfile
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()
    operator.wait_for_operator_webhook_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_internal_auth_keyfile
def test_replica_set_running(mdb: MongoDB):
    # Reaching Running is the functional proof that the members accept each other's certificates: there
    # is no clusterfile, so each member presents the member certificate when it connects to the others.
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_internal_auth_keyfile
def test_only_the_member_certificate_is_issued(namespace: str):
    member, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    assert "tls.crt" in read_secret(namespace, member), f"{member} missing tls.crt"
    for secret_name in [clusterfile, agent]:
        with pytest.raises(ApiException) as e:
            read_secret(namespace, secret_name)
        assert e.value.status == 404, f"{secret_name} should not exist"


@pytest.mark.e2e_replica_set_tls_managedcert_internal_auth_keyfile
def test_member_certificate_carries_the_client_usage(namespace: str):
    # Without a clusterfile the members present the member certificate to each other, so the operator
    # asks the issuer for the client usage as well. tls_rs_managedcert_withhold_client_cert covers the
    # deployment that sets setParameter.tlsWithholdClientCertificate, where it does not.
    assert_member_certificate_client_usage(MDB_RESOURCE, namespace, client_auth=True)


def assert_membership_subject(namespace: str, secret_name: str):
    """The certificate carries the operator's default membership subject O=<resource>-server, OU=<namespace>."""
    subject = subject_from_certificate(namespace, secret_name)
    assert subject.get("organizations") == [f"{MDB_RESOURCE}-server"], subject
    assert subject.get("organizationalUnits") == [namespace], subject


@pytest.mark.e2e_replica_set_tls_managedcert_internal_auth_keyfile
def test_member_certificate_carries_the_membership_subject(namespace: str):
    assert_membership_subject(namespace, leaf_secret_names(MDB_RESOURCE)[0])


@pytest.mark.e2e_replica_set_tls_managedcert_internal_auth_keyfile
def test_switching_to_x509_internal_cluster_auth_is_rejected(mdb: MongoDB):
    mdb.load()
    mdb["spec"]["security"]["authentication"]["internalCluster"] = "X509"

    with pytest.raises(
        ApiException,
        match=(
            r"Cannot change spec\.security\.authentication\.internalCluster "
            r"when spec\.security\.managedCertificate is enabled"
        ),
    ):
        mdb.update()

    mdb.load()
    assert "internalCluster" not in mdb["spec"]["security"]["authentication"]
