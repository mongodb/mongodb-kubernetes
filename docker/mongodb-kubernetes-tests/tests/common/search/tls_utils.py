from cryptography.hazmat.primitives.serialization import (
    BestAvailableEncryption,
    Encoding,
    PrivateFormat,
    load_pem_private_key,
)
from kubetester import create_or_update_secret, read_secret, update_secret
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

# Fixed key inside a dedicated keyFilePassword secret (spec.*.keyFilePasswordSecretRef),
# matching searchcontroller.KeyFilePasswordSecretKey on the operator side.
KEY_FILE_PASSWORD_SECRET_KEY = "keyFilePassword"


def encrypt_tls_key_with_password(namespace: str, secret_name: str, password: str, api_client=None):
    """Encrypts the private key in a TLS secret in place with the given password.

    Reads the secret, re-encrypts tls.key with the password, and writes only the
    encrypted key back. The password itself lives in a separate secret created via
    create_keyfile_password_secret (referenced by spec.*.keyFilePasswordSecretRef)."""
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
        data={"tls.key": encrypted_key_pem.decode()},
        api_client=api_client,
    )
    logger.info(f"Encrypted private key in secret {secret_name} with password")


def create_keyfile_password_secret(namespace: str, password_secret_name: str, password: str, api_client=None):
    """Creates a dedicated secret holding the keyfile password under the fixed
    keyFilePassword key, referenced from the MongoDBSearch CR via
    spec.*.keyFilePasswordSecretRef."""
    create_or_update_secret(
        namespace,
        password_secret_name,
        data={KEY_FILE_PASSWORD_SECRET_KEY: password},
        api_client=api_client,
    )
    logger.info(f"Created keyFilePassword secret {password_secret_name}")
