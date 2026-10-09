#!/usr/bin/env python3

import pytest
from kubetester import read_configmap, read_secret, try_load
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE = "test-tls-rs-managedcert-server"


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str) -> MongoDB:
    # `issuer` fixture installs cert-manager and creates a CA-type Issuer named "ca-issuer".
    resource = MongoDB.from_yaml(load_fixture("test-tls-rs-managedcert.yaml"), name=MDB_RESOURCE, namespace=namespace)
    # Issuer for client certs is configured by user issuer (ca-issuer); the server category has no issuer,
    # so the operator self-signs the server CA.
    resource["spec"]["security"]["managedCertificate"] = {
        "enabled": True,
        "client": {"issuerRef": {"name": "ca-issuer", "kind": "Issuer"}},
    }
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_managedcert_server
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_server
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_server
def test_operator_created_certs_and_split_cas(mdb: MongoDB, namespace: str):
    for secret_name in (
        f"{MDB_RESOURCE}-cert",
        f"{MDB_RESOURCE}-clusterfile",
        f"{MDB_RESOURCE}-agent-certs",
    ):
        cert_secret = read_secret(namespace, secret_name)
        assert "tls.crt" in cert_secret, f"{secret_name} missing tls.crt"
        assert "tls.key" in cert_secret, f"{secret_name} missing tls.key"

    
    server_ca_secret = read_secret(namespace, f"{MDB_RESOURCE}-server-ca")
    assert "tls.crt" in server_ca_secret, f"{MDB_RESOURCE}-server-ca missing tls.crt"

    server_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-server-ca-bundle")
    assert server_ca_cm.get("ca-pem", "").strip() != "", "ca-pem (server CA) missing/empty"
    client_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-client-ca-bundle")
    assert client_ca_cm.get("clusterca-pem", "").strip() != "", "clusterca-pem (client CA) missing/empty"
    assert server_ca_cm["ca-pem"].strip() != client_ca_cm["clusterca-pem"].strip(), (
        "server CA (self-signed) and client CA (ca-issuer) should differ"
    )

    # verify that per category leaf certs are signed by respective CA
    member_crt = read_secret(namespace, f"{MDB_RESOURCE}-cert")["tls.crt"]
    clusterfile_crt = read_secret(namespace, f"{MDB_RESOURCE}-clusterfile")["tls.crt"]
    agent_crt = read_secret(namespace, f"{MDB_RESOURCE}-agent-certs")["tls.crt"]
    server_ca = server_ca_cm["ca-pem"]
    client_ca = client_ca_cm["clusterca-pem"]

    # Server (member) cert: signed by the operator's self-signed server CA, not the client issuer CA.
    assert cert_signed_by_bundle(member_crt, server_ca), "member (server) cert must be signed by the server CA"
    assert not cert_signed_by_bundle(member_crt, client_ca), "member cert must not be signed by the client CA"
    # Client (clusterfile + agent) certs: signed by the user issuer's client CA, not the server CA.
    assert cert_signed_by_bundle(clusterfile_crt, client_ca), "clusterfile cert must be signed by the client CA"
    assert cert_signed_by_bundle(agent_crt, client_ca), "agent cert must be signed by the client CA"
    assert not cert_signed_by_bundle(clusterfile_crt, server_ca), "clusterfile cert must not be signed by the server CA"


@pytest.mark.e2e_replica_set_tls_managedcert_server
@skip_if_local()
def test_mdb_is_not_reachable_without_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_managedcert_server
def test_scale_up(mdb: MongoDB):
    mdb["spec"]["members"] = 5
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_server
def test_scale_down(mdb: MongoDB):
    mdb["spec"]["members"] = 3
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)
