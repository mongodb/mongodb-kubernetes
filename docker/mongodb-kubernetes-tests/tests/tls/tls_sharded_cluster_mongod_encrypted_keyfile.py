#!/usr/bin/env python3
"""Sharded cluster with password-encrypted TLS server private keys on every tier.

Proves the operator supplies net.tls.certificateKeyFilePassword per tier (mongos, config servers,
shards), each with its own cert secret + password. Reaching Running is the proof that all three
tiers' processes decrypted their keys with the operator-injected passwords — this is the case most
likely to expose a missed tier.
"""

import kubernetes
from kubetester import read_secret, try_load
from kubetester.certs import Certificate, create_sharded_cluster_certs
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.search.tls_utils import encrypt_tls_key_with_password
from tests.conftest import central_cluster_client
from tests.shardedcluster.conftest import get_mongos_service_names

MDB_RESOURCE = "sharded-cluster-encrypted-keyfile"
CERT_PREFIX = "prefix"
KEY_FILE_PASSWORD = "test-tls-key-password"

# Per-tier cert secret names produced by create_sharded_cluster_certs with secret_prefix="prefix-".
# These match what the operator resolves via MemberCertificateSecretName(<tier RS name>).
SHARD_CERT_SECRET = f"{CERT_PREFIX}-{MDB_RESOURCE}-0-cert"
CONFIG_CERT_SECRET = f"{CERT_PREFIX}-{MDB_RESOURCE}-config-cert"
MONGOS_CERT_SECRET = f"{CERT_PREFIX}-{MDB_RESOURCE}-mongos-cert"
TIER_CERT_SECRETS = [SHARD_CERT_SECRET, CONFIG_CERT_SECRET, MONGOS_CERT_SECRET]


@fixture(scope="module")
def all_certs(central_cluster_client: kubernetes.client.ApiClient, issuer: str, namespace: str) -> None:
    """Generates server certs for every tier, then encrypts each tier's private key with a password."""
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE,
        shards=1,
        mongod_per_shard=3,
        config_servers=3,
        mongos=2,
        secret_prefix=f"{CERT_PREFIX}-",
    )
    # Each tier's cert is managed by a cert-manager Certificate that keeps reconciling its secret; if
    # we encrypt tls.key while the Certificate still owns the secret, cert-manager re-issues and
    # reverts the key to plaintext. Delete each Certificate (the already-issued secret stays) before
    # encrypting so the encrypted key persists.
    for cert_secret in TIER_CERT_SECRETS:
        Certificate(name=cert_secret, namespace=namespace).delete()
        encrypt_tls_key_with_password(namespace, cert_secret, KEY_FILE_PASSWORD)


@fixture(scope="module")
def sc(namespace: str, issuer_ca_configmap: str, custom_mdb_version: str, all_certs) -> MongoDB:
    resource = MongoDB.from_yaml(
        load_fixture("test-tls-base-sc-require-ssl.yaml"),
        name=MDB_RESOURCE,
        namespace=namespace,
    )

    resource["spec"]["security"] = {
        "tls": {
            "enabled": True,
            "ca": issuer_ca_configmap,
        },
        "certsSecretPrefix": CERT_PREFIX,
    }

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    try_load(resource)
    return resource


@mark.e2e_sharded_cluster_tls_mongod_encrypted_keyfile
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_tls_mongod_encrypted_keyfile
def test_tls_keys_are_encrypted(all_certs, namespace: str):
    # Negative confidence: every tier's key must be a genuinely encrypted PEM, so a green Running
    # below cannot be a false pass with an unencrypted key on any tier.
    for cert_secret in TIER_CERT_SECRETS:
        secret_data = read_secret(namespace, cert_secret)
        assert "ENCRYPTED" in secret_data["tls.key"], f"tls.key in {cert_secret} must be encrypted PEM"
        assert secret_data["tls.keyFilePassword"] == KEY_FILE_PASSWORD


@mark.e2e_sharded_cluster_tls_mongod_encrypted_keyfile
def test_sharded_cluster_gets_to_running_state(sc: MongoDB):
    # Reaching Running proves mongos, config servers, and shard mongods all loaded their encrypted
    # keys with the per-tier operator-injected passwords.
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_sharded_cluster_tls_mongod_encrypted_keyfile
@skip_if_local
def test_sharded_cluster_has_connectivity_with_tls(sc: MongoDB, ca_path: str):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(ca_path=ca_path, use_ssl=True, service_names=service_names)
    tester.assert_connectivity()


@mark.e2e_sharded_cluster_tls_mongod_encrypted_keyfile
@skip_if_local
def test_sharded_cluster_has_no_connectivity_without_tls(sc: MongoDB):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(use_ssl=False, service_names=service_names)
    tester.assert_no_connection()
