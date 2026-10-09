#!/usr/bin/env python3

import pytest
from kubetester import try_load
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE = "test-tls-rs-managedcert-client"


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str) -> MongoDB:
    # `issuer` fixture installs cert-manager and creates a CA-type Issuer named "ca-issuer".
    resource = MongoDB.from_yaml(load_fixture("test-tls-rs-managedcert.yaml"), name=MDB_RESOURCE, namespace=namespace)
    # Issuer for server certs is configured (ca-issuer); the client category has no issuer,
    # so the operator self-signs the client CA.
    resource["spec"]["security"]["managedCertificate"] = {
        "enabled": True,
        "server": {"issuerRef": {"name": "ca-issuer", "kind": "Issuer"}},
    }
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_operator_created_all_cert_secrets_and_ca(mdb: MongoDB, namespace: str):
    from kubetester import read_configmap, read_secret

    # make sure correct component certificate secrets are created
    for secret_name in (
        f"{MDB_RESOURCE}-cert",
        f"{MDB_RESOURCE}-clusterfile",
        f"{MDB_RESOURCE}-agent-certs",
    ):
        cert_secret = read_secret(namespace, secret_name)
        assert "tls.crt" in cert_secret, f"{secret_name} missing tls.crt"
        assert "tls.key" in cert_secret, f"{secret_name} missing tls.key"

    # make sure correct CA configmaps are created per category for the user configure issuer (server)
    # as well as the one that MCK created (client).
    server_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-server-ca-bundle")
    assert server_ca_cm.get("ca-pem", "").strip() != "", "ca-pem (server CA) missing/empty"
    client_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-client-ca-bundle")
    assert client_ca_cm.get("clusterca-pem", "").strip() != "", "clusterca-pem (client self-signed CA) missing/empty"
    # The two categories must have distinct CAs (server from issuer that is user configured, client self-signed).
    assert server_ca_cm["ca-pem"].strip() != client_ca_cm["clusterca-pem"].strip(), (
        "server CA and client CA should differ (server=issuer, client=self-signed)"
    )

    # verify that per category leaf certs are signed/issued by that category's CA because in this test
    # server CA is user configured and client CA is mck managed. It cryptographically verifies that certs
    # are signed by correct CA.
    member_crt = read_secret(namespace, f"{MDB_RESOURCE}-cert")["tls.crt"]
    clusterfile_crt = read_secret(namespace, f"{MDB_RESOURCE}-clusterfile")["tls.crt"]
    agent_crt = read_secret(namespace, f"{MDB_RESOURCE}-agent-certs")["tls.crt"]
    server_ca = server_ca_cm["ca-pem"]
    client_ca = client_ca_cm["clusterca-pem"]

    # Server (member) cert: signed by the user issuer's server CA, not the client self-signed CA.
    assert cert_signed_by_bundle(member_crt, server_ca), "member (server) cert must be signed by the server CA"
    assert not cert_signed_by_bundle(member_crt, client_ca), "member cert must not be signed by the client CA"
    # Client (clusterfile + agent) certs: signed by the operator's client self-signed CA, not the server CA.
    assert cert_signed_by_bundle(clusterfile_crt, client_ca), "clusterfile cert must be signed by the client CA"
    assert cert_signed_by_bundle(agent_crt, client_ca), "agent cert must be signed by the client CA"
    assert not cert_signed_by_bundle(clusterfile_crt, server_ca), "clusterfile cert must not be signed by the server CA"


@pytest.mark.e2e_replica_set_tls_managedcert_client
@skip_if_local()
def test_mdb_is_not_reachable_without_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_managedcert_client
@skip_if_local()
def test_mdb_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_scale_up(mdb: MongoDB):
    mdb["spec"]["members"] = 5
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_client
@skip_if_local()
def test_reachable_with_ssl_after_scale_up(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_client
def test_scale_down(mdb: MongoDB):
    mdb["spec"]["members"] = 3
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)
