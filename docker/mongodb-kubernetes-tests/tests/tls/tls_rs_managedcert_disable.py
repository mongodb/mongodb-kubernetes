"""Disabling TLS by removing the managedCertificate block.

The block enables TLS on its own: with tls.enabled off and no certificates from the user, the
deployment gets its TLS entirely from the operator-managed certificates. Removing the block therefore
turns TLS off with it, and the deployment has to come back up without TLS: the pods restart without
the certificate mounts, and the automation config has to stop naming the certificate files before
they are unmounted, or the agents and the readiness probe never reach goal state.

The objects the operator created are left in the cluster when the block is removed. The last test
pins that, so the behaviour stays deliberate: they are removed when the resource is deleted, through
the owner reference the operator puts on them.
"""

import tempfile

import pytest
from kubetester import read_configmap, read_secret, try_load
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import managed_certificate_operator

MDB_RESOURCE = "test-tls-rs-managedcert-disable"


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
    # TLS comes from the managed certificates alone. Removing the block later is then the same edit
    # as turning TLS off, which is the scenario this module covers.
    resource["spec"]["security"]["tls"] = {"enabled": False}
    resource["spec"]["security"]["managedCertificate"] = {}
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_managedcert_disable
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_disable
def test_replica_set_running_with_managed_certificates(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_disable
def test_application_connects_with_tls(mdb: MongoDB, namespace: str):
    # The application validates the deployment with the CA the operator published: the server CA
    # bundle carries it under the fixed ca-pem key, next to the copy mongod mounts by hash.
    server_ca = read_configmap(namespace, f"{MDB_RESOURCE}-managed-server-ca-bundle")["ca-pem"]
    with tempfile.NamedTemporaryFile(mode="w", suffix=".pem", delete=False) as ca_file:
        ca_file.write(server_ca)

    mdb.tester(use_ssl=True, ca_path=ca_file.name).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_disable
def test_disable_by_removing_the_block(mdb: MongoDB):
    # update() sends a merge patch, so a field is removed by setting it to None. With tls.enabled
    # already off, this leaves the deployment with no TLS at all.
    mdb["spec"]["security"]["managedCertificate"] = None
    mdb.update()

    # The pods roll without the certificate mounts, and the automation config drops the certificate
    # files. Reaching Running is the proof that the two happened in an order the agents survive.
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_disable
def test_application_connects_without_tls(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_disable
def test_certificates_ready_condition_is_gone(mdb: MongoDB):
    # The condition reports the state of the operator-managed certificates, so it goes with the block
    # that asked for them: a deployment without them must not keep reporting that they are ready.
    mdb.load()
    conditions = mdb["status"].get("conditions", [])
    assert [c for c in conditions if c["type"] == "CertificatesReady"] == [], conditions


@pytest.mark.e2e_replica_set_tls_managedcert_disable
def test_what_the_operator_created_is_left_in_place(namespace: str):
    # Disabling the feature does not delete what it created. The Certificates and their secrets are
    # removed when the resource is deleted, through the owner reference the operator puts on them.
    assert "tls.crt" in read_secret(namespace, f"{MDB_RESOURCE}-managed-cert")
    assert "latestHash" in read_secret(namespace, f"{MDB_RESOURCE}-managed-cert-pem")
    assert "ca-pem" in read_configmap(namespace, f"{MDB_RESOURCE}-managed-server-ca-bundle")
