from kubernetes import client

from cryptography import x509
from cryptography.x509.oid import NameOID
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.backends import default_backend
from cryptography.hazmat.primitives.asymmetric import rsa

import base64

from typing import List


def generate_csr(namespace: str, host: str, servicename: str):
    key = rsa.generate_private_key(
        public_exponent=65537,
        key_size=2048,
        backend=default_backend()
    )

    csr = x509.CertificateSigningRequestBuilder().subject_name(x509.Name([
        x509.NameAttribute(NameOID.COUNTRY_NAME, u"US"),
        x509.NameAttribute(NameOID.STATE_OR_PROVINCE_NAME, u"New York"),
        x509.NameAttribute(NameOID.LOCALITY_NAME, u"New York"),
        x509.NameAttribute(NameOID.ORGANIZATION_NAME, u"Mongodb"),
        x509.NameAttribute(NameOID.COMMON_NAME, host),
    ])).add_extension(
        x509.SubjectAlternativeName([
            x509.DNSName(f"{host}."),
            x509.DNSName(f"{host}.{servicename}.{namespace}.svc.cluster.local"),
            x509.DNSName(f"{servicename}.{namespace}.svc.cluster.local"),
        ]),
        critical=False,
    ).sign(key, hashes.SHA256(), default_backend())

    return (
        csr.public_bytes(serialization.Encoding.PEM),
        key.private_bytes(encoding=serialization.Encoding.PEM,
                          format=serialization.PrivateFormat.TraditionalOpenSSL,
                          encryption_algorithm=serialization.NoEncryption())
    )


def request_certificate(csr: [bytes], name: str, usages: List[str]) -> client.V1beta1CertificateSigningRequest:
    request = client.V1beta1CertificateSigningRequest(
        spec=client.V1beta1CertificateSigningRequestSpec(
            request=base64.b64encode(csr).decode(),
            usages=usages,
        )
    )
    request.metadata = client.V1ObjectMeta()
    request.metadata.name = name

    return client.CertificatesV1beta1Api().create_certificate_signing_request(request)


def get_pem_certificate(name: str) -> str:
    body = client.CertificatesV1beta1Api().read_certificate_signing_request_status(name)
    return base64.b64decode(body.status.certificate)
