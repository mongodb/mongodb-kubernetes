"""A TLS replica set with operator-managed certificates whose mongod configuration withholds the client certificate.

Internal cluster authentication is SCRAM, so there is no clusterfile and, by default, mongod also
presents the member certificate when it connects to the other members. That is why the operator
normally requests that certificate with both the server and the client usage. With
setParameter.tlsWithholdClientCertificate the members present nothing, so the operator requests the
server usage only, and a customer can use an issuer that cannot issue clientAuth certificates.

Reaching Running is the proof that withholding works the way the operator assumes: the member
certificate has no client usage, so a member that still presented it to its peers would be rejected by
them, and the replica set would never form.
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
)

MDB_RESOURCE = "test-tls-rs-managedcert-withhold"


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
    # The members do not present their certificate when they connect to each other.
    resource["spec"]["additionalMongodConfig"] = {"setParameter": {"tlsWithholdClientCertificate": True}}
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_managedcert_withhold_client_cert
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_withhold_client_cert
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_withhold_client_cert
def test_member_certificate_has_no_client_usage(namespace: str):
    assert_member_certificate_client_usage(MDB_RESOURCE, namespace, client_auth=False)


@pytest.mark.e2e_replica_set_tls_managedcert_withhold_client_cert
def test_only_the_member_certificate_is_issued(namespace: str):
    # SCRAM internal auth and no x509 agent: there is no clusterfile and no agent certificate.
    _, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    for secret_name in (clusterfile, agent):
        with pytest.raises(ApiException) as e:
            read_secret(namespace, secret_name)
        assert e.value.status == 404, f"{secret_name} should not exist"
