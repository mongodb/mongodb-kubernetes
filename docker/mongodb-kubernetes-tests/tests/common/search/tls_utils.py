from typing import Optional

import kubernetes
from cryptography.hazmat.primitives.serialization import (
    BestAvailableEncryption,
    Encoding,
    PrivateFormat,
    load_pem_private_key,
)
from kubetester import create_or_update_secret, read_secret, update_secret
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

# Fixed key inside the keyfile-password secret. Mirrors the operator constant
# certs.KeyFilePasswordSecretKey.
KEY_FILE_PASSWORD_SECRET_KEY = "keyFilePassword"


def encrypt_tls_key_with_password(
    namespace: str,
    secret_name: str,
    password: str,
    password_secret_name: Optional[str] = None,
    api_client: Optional[kubernetes.client.ApiClient] = None,
):
    """Encrypts the private key in a TLS cert secret with a password.

    Reads the cert secret, encrypts tls.key in place with the given password, and — when
    password_secret_name is provided — writes the password into a SEPARATE secret under the
    "keyFilePassword" key.

    Pass api_client to target a non-default cluster (e.g. the central cluster for a multi-cluster
    source secret). For multi-cluster, create the password secret in every member cluster separately
    (see create_keyfile_password_secret)."""
    secret_data = read_secret(namespace, secret_name, api_client=api_client)

    private_key = load_pem_private_key(secret_data["tls.key"].encode(), password=None)
    encrypted_key_pem = private_key.private_bytes(
        encoding=Encoding.PEM,
        format=PrivateFormat.TraditionalOpenSSL,
        encryption_algorithm=BestAvailableEncryption(password.encode()),
    )

    update_secret(
        namespace,
        secret_name,
        data={
            "tls.key": encrypted_key_pem.decode(),
        },
        api_client=api_client,
    )
    logger.info(f"Encrypted private key in cert secret {secret_name} with password")

    if password_secret_name is not None:
        create_keyfile_password_secret(namespace, password_secret_name, password, api_client=api_client)


def create_keyfile_password_secret(
    namespace: str,
    password_secret_name: str,
    password: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
):
    """Creates (or updates) the dedicated keyfile-password secret holding the password under the
    fixed "keyFilePassword" key. This is the secret the operator reads via the search CR's
    spec.*.keyFilePasswordSecretRef (or the mongod security.keyFilePasswordSecretPrefix)."""
    create_or_update_secret(
        namespace,
        password_secret_name,
        data={KEY_FILE_PASSWORD_SECRET_KEY: password},
        api_client=api_client,
    )
    logger.info(f"Created keyfile-password secret {password_secret_name}")
