import socket
import ssl
from datetime import datetime, timedelta, timezone
from typing import cast

from cryptography import x509
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric.types import CertificateIssuerPrivateKeyTypes
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


def create_crl(
    ca_cert_path: str,
    ca_key_path: str,
    revoked_serials: list[int],
    next_update_days: int = 30,
) -> str:
    """Builds a PEM CRL signed by the CA at the given paths, revoking the given serials.

    The CRL issuer is the CA certificate's subject, so it validates the leaf certificates
    issued by the same CA (cert-manager issues them from the ca-tls key pair)."""
    with open(ca_cert_path, "rb") as f:
        ca_cert = x509.load_pem_x509_certificate(f.read())
    with open(ca_key_path, "rb") as f:
        ca_key = cast(CertificateIssuerPrivateKeyTypes, load_pem_private_key(f.read(), password=None))

    now = datetime.now(timezone.utc)
    builder = (
        x509.CertificateRevocationListBuilder()
        .issuer_name(ca_cert.subject)
        .last_update(now)
        .next_update(now + timedelta(days=next_update_days))
    )
    for serial in revoked_serials:
        revoked = x509.RevokedCertificateBuilder().serial_number(serial).revocation_date(now).build()
        builder = builder.add_revoked_certificate(revoked)

    crl = builder.sign(private_key=ca_key, algorithm=hashes.SHA256())
    logger.info(f"Built CRL revoking {len(revoked_serials)} serial(s)")
    return crl.public_bytes(Encoding.PEM).decode()


def create_invalid_crl() -> str:
    """Returns a syntactically invalid CRL PEM blob (correct header/footer, junk body)."""
    return "-----BEGIN X509 CRL-----\nnot a valid crl body\n-----END X509 CRL-----\n"


def cert_serial_number(cert_pem: str) -> int:
    """Returns the serial number of a PEM-encoded certificate."""
    return x509.load_pem_x509_certificate(cert_pem.encode()).serial_number


def assert_tls_handshake(
    host: str,
    port: int,
    cert_path: str,
    key_path: str,
    ca_path: str,
    should_succeed: bool,
    timeout: float = 10.0,
) -> None:
    """Opens a raw TLS connection to host:port presenting the given client cert/key and
    verifying the server against ca_path. ALPN is pinned to h2 to match mongot's gRPC listener.

    Asserts the handshake succeeds when should_succeed, otherwise that it raises
    ssl.SSLError/OSError (e.g. because the presented client certificate is revoked)."""
    context = ssl.create_default_context(cafile=ca_path)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.set_alpn_protocols(["h2"])
    context.load_cert_chain(certfile=cert_path, keyfile=key_path)

    handshake_error: Exception | None = None
    try:
        with socket.create_connection((host, port), timeout=timeout) as raw_socket:
            with context.wrap_socket(raw_socket, server_hostname=host):
                pass
    except (ssl.SSLError, OSError) as e:
        handshake_error = e

    if should_succeed:
        assert handshake_error is None, f"TLS handshake to {host}:{port} failed: {handshake_error!r}"
        logger.info(f"TLS handshake to {host}:{port} succeeded")
    else:
        assert handshake_error is not None, f"TLS handshake to {host}:{port} unexpectedly succeeded"
        logger.info(f"TLS handshake to {host}:{port} failed as expected: {handshake_error!r}")
