import base64
import time

from cryptography import x509
from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.backends import default_backend
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec, padding, rsa
from cryptography.x509.oid import NameOID
from kubernetes import client


def generate_csr(namespace: str, host: str, servicename: str):
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048, backend=default_backend())

    csr = (
        x509.CertificateSigningRequestBuilder()
        .subject_name(
            x509.Name(
                [
                    x509.NameAttribute(NameOID.COUNTRY_NAME, "US"),
                    x509.NameAttribute(NameOID.STATE_OR_PROVINCE_NAME, "New York"),
                    x509.NameAttribute(NameOID.LOCALITY_NAME, "New York"),
                    x509.NameAttribute(NameOID.ORGANIZATION_NAME, "Mongodb"),
                    x509.NameAttribute(NameOID.COMMON_NAME, host),
                ]
            )
        )
        .add_extension(
            x509.SubjectAlternativeName(
                [
                    x509.DNSName(f"{host}."),
                    x509.DNSName(f"{host}.{servicename}.{namespace}.svc.cluster.local"),
                    x509.DNSName(f"{servicename}.{namespace}.svc.cluster.local"),
                ]
            ),
            critical=False,
        )
        .sign(key, hashes.SHA256(), default_backend())
    )

    return (
        csr.public_bytes(serialization.Encoding.PEM),
        key.private_bytes(
            encoding=serialization.Encoding.PEM,
            format=serialization.PrivateFormat.TraditionalOpenSSL,
            encryption_algorithm=serialization.NoEncryption(),
        ),
    )


def get_pem_certificate(name: str) -> bytes | None:
    body = client.CertificatesV1Api().read_certificate_signing_request_status(name)
    if body.status.certificate is None:
        return None
    return base64.b64decode(body.status.certificate)


def _ca_signed_leaf(ca: x509.Certificate, leaf: x509.Certificate) -> bool:
    # Return True if `ca` actually signed `leaf` (cryptographic check, not just a name match).
    public_key = ca.public_key()
    try:
        if isinstance(public_key, rsa.RSAPublicKey):
            public_key.verify(
                leaf.signature,
                leaf.tbs_certificate_bytes,
                padding.PKCS1v15(),
                leaf.signature_hash_algorithm,
            )
        elif isinstance(public_key, ec.EllipticCurvePublicKey):
            public_key.verify(
                leaf.signature,
                leaf.tbs_certificate_bytes,
                ec.ECDSA(leaf.signature_hash_algorithm),
            )
        else:  # ed25519 / ed448 take just (signature, data)
            public_key.verify(leaf.signature, leaf.tbs_certificate_bytes)
        return True
    except InvalidSignature:
        return False


def cert_signed_by_bundle(leaf_pem: str, ca_bundle_pem: str) -> bool:
    # True if any CA in the PEM bundle signed the leaf certificate. The leaf's tls.crt may
    # hold a chain; cert-manager puts the signed leaf first, so we load that one. A bundle may
    # hold several CAs, so the leaf is valid if ANY of them signed it.
    leaf = x509.load_pem_x509_certificate(leaf_pem.encode(), default_backend())
    for block in ca_bundle_pem.split("-----END CERTIFICATE-----"):
        block = block.strip()
        if not block:
            continue
        ca = x509.load_pem_x509_certificate((block + "\n-----END CERTIFICATE-----\n").encode(), default_backend())
        if _ca_signed_leaf(ca, leaf):
            return True
    return False


def wait_for_certs_to_be_issued(certificates: list[str]) -> None:
    un_issued_certs = set(certificates)
    while un_issued_certs:
        issued_certs = set()
        to_wait = False
        for cert in un_issued_certs:
            if get_pem_certificate(cert):
                issued_certs.add(cert)
            else:
                print(f"waiting for certificate {cert} to be issued")
                to_wait = True
        un_issued_certs -= issued_certs
        if to_wait:
            time.sleep(1)
