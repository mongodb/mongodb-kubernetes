import base64
import datetime
import time
from typing import List, Optional

from cryptography import x509
from cryptography.hazmat.backends import default_backend
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
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


def generate_self_signed_ca_pem(common_name: str) -> bytes:
    """Returns a PEM encoded, self signed CA certificate.

    The certificate signs nothing, it only has to be a structurally valid CA: something an x509 cert
    pool accepts, so it can stand in for a trust anchor the test does not control.
    """
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048, backend=default_backend())
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, common_name)])
    now = datetime.datetime.now(datetime.timezone.utc)

    certificate = (
        x509.CertificateBuilder()
        .subject_name(subject)
        .issuer_name(subject)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - datetime.timedelta(minutes=5))
        .not_valid_after(now + datetime.timedelta(days=1))
        .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
        .sign(key, hashes.SHA256(), default_backend())
    )

    return certificate.public_bytes(serialization.Encoding.PEM)


def get_pem_certificate(name: str) -> Optional[bytes]:
    body = client.CertificatesV1Api().read_certificate_signing_request_status(name)
    if body.status.certificate is None:
        return None
    return base64.b64decode(body.status.certificate)


def wait_for_certs_to_be_issued(certificates: List[str]) -> None:
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
