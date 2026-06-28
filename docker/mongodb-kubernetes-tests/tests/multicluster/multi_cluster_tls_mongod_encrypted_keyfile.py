"""Multi-cluster replica set (MongoDBMulti) with a password-encrypted TLS server private key.

Proves the operator supplies net.tls.certificateKeyFilePassword for a MongoDBMulti resource so that
the mongod processes spread across member clusters can decrypt a password-encrypted PEM key at
startup.
"""

from typing import List

import kubernetes
from kubetester import read_secret, try_load
from kubetester.certs import Certificate
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.search.tls_utils import encrypt_tls_key_with_password
from tests.multicluster.conftest import cluster_spec_list

CERT_SECRET_PREFIX = "clustercert"
KEY_FILE_PASSWORD_PREFIX = "kfp"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
BUNDLE_PEM_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert-pem"
KEY_FILE_PASSWORD_SECRET_NAME = f"{KEY_FILE_PASSWORD_PREFIX}-{MDB_RESOURCE}-keyfile-password"
KEY_FILE_PASSWORD = "test-tls-key-password"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names, custom_mdb_version: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    return resource


@fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
):
    create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )

    # The source-of-truth cert lives in the central cluster as a cert-manager Certificate that keeps
    # reconciling its secret; if we encrypt tls.key while it still owns the secret, cert-manager
    # re-issues and reverts the key to plaintext. Delete the Certificate (the already-issued secret
    # stays) before encrypting so the encrypted key persists. The password is written to a SEPARATE
    # secret in the central cluster (the operator reads it via r.SecretClient and propagates the
    # password into the automation config for every member cluster's mongod, so it does not need to be
    # replicated to member clusters).
    Certificate(name=BUNDLE_SECRET_NAME, namespace=namespace, api_client=central_cluster_client).delete()
    encrypt_tls_key_with_password(
        namespace,
        BUNDLE_SECRET_NAME,
        KEY_FILE_PASSWORD,
        password_secret_name=KEY_FILE_PASSWORD_SECRET_NAME,
        api_client=central_cluster_client,
    )
    return BUNDLE_SECRET_NAME


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_multi_unmarshalled: MongoDBMulti,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDBMulti:
    resource = mongodb_multi_unmarshalled
    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "keyFilePasswordSecretPrefix": KEY_FILE_PASSWORD_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@mark.e2e_multi_cluster_tls_mongod_encrypted_keyfile
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_tls_mongod_encrypted_keyfile
def test_tls_key_is_encrypted(server_certs: str, namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    # Negative confidence: ensure the source key is genuinely encrypted, so a green Running below
    # cannot be a false pass with an unencrypted key (which would make the password irrelevant).
    secret_data = read_secret(namespace, BUNDLE_SECRET_NAME, api_client=central_cluster_client)
    assert "ENCRYPTED" in secret_data["tls.key"], "tls.key must be a password-encrypted PEM"
    # The password must live in the dedicated secret, NOT alongside the encrypted key in the cert secret.
    assert "tls.keyFilePassword" not in secret_data, "password must not be stored in the cert secret"
    password_secret = read_secret(namespace, KEY_FILE_PASSWORD_SECRET_NAME, api_client=central_cluster_client)
    assert password_secret["keyFilePassword"] == KEY_FILE_PASSWORD


@mark.e2e_multi_cluster_tls_mongod_encrypted_keyfile
def test_create_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    server_certs: str,
    multi_cluster_issuer_ca_configmap: str,
    member_cluster_clients: List[MultiClusterClient],
):
    # Reaching Running proves the mongod processes on every member cluster loaded their encrypted key
    # using the password the operator injected into the automation config.
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)

    # The operator-managed PEM (cert+encrypted key) must be distributed to every member cluster.
    for client in member_cluster_clients:
        read_secret(
            namespace=namespace,
            name=BUNDLE_PEM_SECRET_NAME,
            api_client=client.api_client,
        )
