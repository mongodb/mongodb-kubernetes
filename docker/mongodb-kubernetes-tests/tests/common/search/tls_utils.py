from cryptography.hazmat.primitives.serialization import (
    BestAvailableEncryption,
    Encoding,
    PrivateFormat,
    load_pem_private_key,
)
from kubetester import read_secret, update_secret
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


def encrypt_tls_key_with_password(namespace: str, secret_name: str, password: str):
    """Encrypts the private key in a TLS secret with a password.

    Reads the secret, encrypts tls.key with the given password, and updates
    the secret with the encrypted key and a tls.keyFilePassword entry
    containing the password."""
    secret_data = read_secret(namespace, secret_name)

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
            "tls.keyFilePassword": password,
        },
    )
    logger.info(f"Encrypted private key in secret {secret_name} with password")
