#!/usr/bin/env python3

import pytest
from kubetester import read_configmap, read_secret, try_load
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE = "test-tls-rs-managedcert"


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-tls-rs-managedcert.yaml"), name=MDB_RESOURCE, namespace=namespace)
    # No issuer for either category: the operator self-signs both the server and the client CAs.
    resource["spec"]["security"]["managedCertificate"] = {"enabled": True}
    try_load(resource)
    return resource


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
    for secret_name in (
        f"{MDB_RESOURCE}-cert",
        f"{MDB_RESOURCE}-clusterfile",
        f"{MDB_RESOURCE}-agent-certs",
    ):
        cert_secret = read_secret(namespace, secret_name)
        assert "tls.crt" in cert_secret, f"{secret_name} missing tls.crt"
        assert "tls.key" in cert_secret, f"{secret_name} missing tls.key"

    # Because no issuer is configured, the operator provisioned both per-category
    # self-signed root CAs (server signs the member cert, client signs clusterfile/agent).
    for ca_secret_name in (
        f"{MDB_RESOURCE}-server-ca",
        f"{MDB_RESOURCE}-client-ca",
    ):
        ca_secret = read_secret(namespace, ca_secret_name)
        assert "tls.crt" in ca_secret, f"{ca_secret_name} missing tls.crt"

    # The two operator-owned per-category CA bundle ConfigMaps carry the two trust stores:
    #   <name>-managed-server-ca-bundle, key ca-pem (server self-signed CA that is configured as net.tls.CAFile)
    #   <name>-managed-client-ca-bundle, key clusterca-pem (client self-signed CA that is configured as
    #     net.tls.clusterCAFile)
    # They must be present, non-empty, and distinct.
    server_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-server-ca-bundle")
    assert server_ca_cm.get("ca-pem", "").strip() != "", "ca-pem (server CA) missing/empty"
    client_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-client-ca-bundle")
    assert client_ca_cm.get("clusterca-pem", "").strip() != "", "clusterca-pem (client CA) missing/empty"
    assert server_ca_cm["ca-pem"].strip() != client_ca_cm["clusterca-pem"].strip(), (
        "server and client self-signed CAs should be distinct roots"
    )


@pytest.mark.e2e_replica_set_tls_managedcert
@skip_if_local()
def test_mdb_is_not_reachable_without_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_managedcert
def test_scale_up(mdb: MongoDB):
    mdb["spec"]["members"] = 5
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert
def test_scale_down_needs_no_new_certs(mdb: MongoDB):
    mdb["spec"]["members"] = 3
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert
def test_scale_up_within_peak_does_not_reissue(mdb: MongoDB, namespace: str):
    cert_before = read_secret(namespace, f"{MDB_RESOURCE}-cert")["tls.crt"]

    mdb["spec"]["members"] = 4
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)

    # The cluster peaked at 5 members earlier, then scaled down to 3. The operator does not
    # shrink the member cert SANs on scale-down, so it still covers members 0-4. Scaling back
    # up to 4 reuses a name already in the cert, so the cert must not be reissued: its content
    # is byte-for-byte the same. No reissue means no trigger to restart or roll the members.
    cert_after = read_secret(namespace, f"{MDB_RESOURCE}-cert")["tls.crt"]
    assert cert_after == cert_before, "member cert must not be reissued on a scale-up within the SAN peak"
