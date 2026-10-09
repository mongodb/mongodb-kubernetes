#!/usr/bin/env python3

import pytest
from kubetester import read_configmap, read_secret, try_load
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE = "test-tls-rs-managedcert-both-configured"


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str) -> MongoDB:
    # The `issuer` fixture installs cert-manager and creates the CA-type "ca-issuer".
    resource = MongoDB.from_yaml(load_fixture("test-tls-rs-managedcert.yaml"), name=MDB_RESOURCE, namespace=namespace)
    issuer_ref = {"issuerRef": {"name": "ca-issuer", "kind": "Issuer"}}
    # Both categories are configured in the MDB resource (user configured), MCK will not setup any managed self signed CA
    resource["spec"]["security"]["managedCertificate"] = {"enabled": True, "server": issuer_ref, "client": issuer_ref}
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_operator_created_certs_from_one_issuer(mdb: MongoDB, namespace: str):
    # All three (member, clusterfile, agent) leaf certs are issued as kubernetes.io/tls
    # secrets at the derived names, with no user-created secrets.
    for secret_name in (
        f"{MDB_RESOURCE}-cert",
        f"{MDB_RESOURCE}-clusterfile",
        f"{MDB_RESOURCE}-agent-certs",
    ):
        cert_secret = read_secret(namespace, secret_name)
        assert "tls.crt" in cert_secret, f"{secret_name} missing tls.crt"
        assert "tls.key" in cert_secret, f"{secret_name} missing tls.key"

    # Certs of both categories are signed by the user-configured issuer. The operator creates the
    # two per-category CA bundle ConfigMaps from the ca.crt field that cert-manager writes into
    # each issued leaf cert secret. Because the same issuer signs both in our test, both leaf secrets
    # carry the same ca.crt, so the server bundle (ca-pem key to net.tls.CAFile in mongod config)
    # and the client bundle (clusterca-pem to net.tls.clusterCAFile) hold the same CA.
    server_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-server-ca-bundle")
    assert server_ca_cm.get("ca-pem", "").strip() != "", "ca-pem (server CA) missing/empty"
    client_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-client-ca-bundle")
    assert client_ca_cm.get("clusterca-pem", "").strip() != "", "clusterca-pem (client CA) missing/empty"
    assert server_ca_cm["ca-pem"].strip() == client_ca_cm["clusterca-pem"].strip(), (
        "one issuer signs both categories, so the server and client CA bundles should match"
    )


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
@skip_if_local()
def test_mdb_is_not_reachable_without_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
@skip_if_local()
def test_mdb_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    # The server certs are signed by ca-issuer, so a client trusting ca-issuer's CA
    # (ca_path) can verify the connection.
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_scale_up(mdb: MongoDB):
    mdb["spec"]["members"] = 5
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
@skip_if_local()
def test_reachable_with_ssl_after_scale_up(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_managedcert_both_configured
def test_scale_down(mdb: MongoDB):
    mdb["spec"]["members"] = 3
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)
