"""A TLS replica set with operator-managed certificates and SCRAM, where the members present a
clusterfile to each other even though internal cluster authentication is not x509.

The members authenticate each other with the keyfile, but with TLS on their connections are mutual
TLS, so each member still presents a client certificate. Here that is the clusterfile, signed by the
client CA, and the member certificate stays a server-only certificate. The automation config sets
net.tls.clusterFile and net.tls.clusterCAFile, but not clusterAuthMode.
"""

import pytest
from cryptography.x509.oid import ExtendedKeyUsageOID
from kubernetes.client.rest import ApiException
from kubetester import read_secret, try_load
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import (
    assert_clusterfile_without_x509,
    extended_key_usages,
    leaf_secret_names,
    managed_certificate_operator,
    read_ca_bundles,
)

MDB_RESOURCE = "test-tls-rs-managedcert-clusterfile"


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
    # No issuers, so the operator self-signs both the server CA and the client CA.
    resource["spec"]["security"]["managedCertificate"] = {}
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_managedcert_clusterfile_without_x509
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_clusterfile_without_x509
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_clusterfile_without_x509
def test_member_and_clusterfile_certificates_are_issued(namespace: str):
    member, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    for secret_name in [member, clusterfile]:
        assert "tls.crt" in read_secret(namespace, secret_name), f"{secret_name} missing tls.crt"
    # The agent uses SCRAM, so it has no certificate.
    with pytest.raises(ApiException) as e:
        read_secret(namespace, agent)
    assert e.value.status == 404, f"{agent} should not exist"


@pytest.mark.e2e_replica_set_tls_managedcert_clusterfile_without_x509
def test_member_certificate_is_server_only_and_clusterfile_is_client(namespace: str):
    member, clusterfile, _ = leaf_secret_names(MDB_RESOURCE)
    member_usages = extended_key_usages(namespace, member)
    assert ExtendedKeyUsageOID.SERVER_AUTH in member_usages, f"{member} does not allow serverAuth"
    assert ExtendedKeyUsageOID.CLIENT_AUTH not in member_usages, f"{member} should be server-only"
    assert ExtendedKeyUsageOID.CLIENT_AUTH in extended_key_usages(
        namespace, clusterfile
    ), f"{clusterfile} does not allow clientAuth"


@pytest.mark.e2e_replica_set_tls_managedcert_clusterfile_without_x509
def test_certificates_are_signed_by_their_category_ca(namespace: str):
    member, clusterfile, _ = leaf_secret_names(MDB_RESOURCE)
    server_ca, client_ca = read_ca_bundles(MDB_RESOURCE, namespace)
    assert cert_signed_by_bundle(
        read_secret(namespace, member)["tls.crt"], server_ca
    ), "member cert must be signed by the server CA"
    assert cert_signed_by_bundle(
        read_secret(namespace, clusterfile)["tls.crt"], client_ca
    ), "clusterfile must be signed by the client CA"


@pytest.mark.e2e_replica_set_tls_managedcert_clusterfile_without_x509
def test_automation_config_sets_the_clusterfile_without_x509(mdb: MongoDB):
    assert_clusterfile_without_x509(mdb.get_automation_config_tester().automation_config, MDB_RESOURCE, 3)
