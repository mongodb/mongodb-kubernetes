#!/usr/bin/env python3
"""Replica set with a password-encrypted TLS server private key.

Proves the operator supplies net.tls.certificateKeyFilePassword so that mongod can decrypt a
password-encrypted PEM key at startup. Without that wiring the processes cannot load the key and the
resource never reaches Running.
"""

import pytest
from kubetester import read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, Certificate, create_mongodb_tls_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.common.search.tls_utils import encrypt_tls_key_with_password

MDB_RESOURCE = "test-tls-base-rs-require-ssl"
CERT_PREFIX = "prefix"
CERT_SECRET_NAME = f"{CERT_PREFIX}-{MDB_RESOURCE}-cert"
KEY_FILE_PASSWORD = "test-tls-key-password"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    secret_name = create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MDB_RESOURCE,
        CERT_SECRET_NAME,
        replicas=3,
    )
    # The cert is managed by a cert-manager Certificate which keeps reconciling its secret; if we
    # encrypt tls.key while it still owns the secret, cert-manager re-issues and reverts the key back
    # to plaintext. Delete the Certificate (the already-issued secret stays) before encrypting so the
    # encrypted key persists.
    Certificate(name=CERT_SECRET_NAME, namespace=namespace).delete()
    # Replace tls.key with a password-encrypted PEM and write tls.keyFilePassword into the same secret.
    encrypt_tls_key_with_password(namespace, CERT_SECRET_NAME, KEY_FILE_PASSWORD)
    return secret_name


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-tls-base-rs-require-ssl.yaml"), namespace=namespace)
    resource["spec"]["security"]["tls"] = {"ca": issuer_ca_configmap}
    # Setting security.certsSecretPrefix implicitly enables TLS
    resource["spec"]["security"]["certsSecretPrefix"] = CERT_PREFIX
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_encrypted_keyfile
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_replica_set_tls_encrypted_keyfile
def test_tls_key_is_encrypted(server_certs: str, namespace: str):
    # Negative confidence: ensure the key is genuinely encrypted, so a green Running below cannot be a
    # false pass with an unencrypted key (which would make the password irrelevant).
    secret_data = read_secret(namespace, CERT_SECRET_NAME)
    assert "ENCRYPTED" in secret_data["tls.key"], "tls.key must be a password-encrypted PEM"
    assert secret_data["tls.keyFilePassword"] == KEY_FILE_PASSWORD


@pytest.mark.e2e_replica_set_tls_encrypted_keyfile
def test_replica_set_running(mdb: MongoDB):
    # Reaching Running proves every mongod loaded its encrypted key using the operator-injected password.
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_tls_encrypted_keyfile
@skip_if_local()
def test_mdb_is_not_reachable_without_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_encrypted_keyfile
@skip_if_local()
def test_mdb_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()
