"""
Certificate Custom Resource Definition.
"""

import collections

from datetime import datetime, timezone
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import random_k8s_name, create_secret, read_secret
from typing import Optional, Dict, List, Generator
from kubeobject import CustomObject
import time
import random


ISSUER_CA_NAME = "ca-issuer"


# Defines properties of a set of servers, like a Shard, or Replica Set holding config servers.
# This is almost equivalent to the StatefulSet created.
SetProperties = collections.namedtuple("SetProperties", ["name", "service", "replicas"])


CertificateType = CustomObject.define(
    "Certificate", plural="certificates", group="cert-manager.io", version="v1alpha2"
)


class WaitForConditions:
    def is_ready(self):
        self.reload()

        if "status" not in self:
            return

        for condition in self["status"]["conditions"]:
            if (
                condition["reason"] == self.Reason
                and condition["status"] == "True"
                and condition["type"] == "Ready"
            ):
                return True

    def block_until_ready(self):
        while not self.is_ready():
            time.sleep(2)


class Certificate(CertificateType, WaitForConditions):
    Reason = "Ready"


IssuerType = CustomObject.define(
    "Issuer", plural="issuers", group="cert-manager.io", version="v1alpha2"
)


class Issuer(IssuerType, WaitForConditions):
    Reason = "KeyPairVerified"


def generate_cert(
    namespace: str, pod: str, pod_dns: str, issuer: str, spec: Optional[Dict] = None
):
    if spec is None:
        spec = dict()
    secret_name = "{}-{}".format(pod, random_k8s_name(prefix="")[:4])
    cert = Certificate(namespace=namespace, name=secret_name)
    cert["spec"] = {
        "dnsNames": [pod_dns, pod],
        "secretName": secret_name,
        "issuerRef": {"name": issuer},
        "duration": "240h",
        "renewBefore": "120h",
        "usages": ["server auth", "client auth"],
    }
    cert["spec"].update(spec)
    cert.create().block_until_ready()

    # Make sure the Secret names used have a random part
    return secret_name


def create_tls_certs(
    issuer: str,
    namespace: str,
    resource_name: str,
    bundle_secret_name: str,
    replicas: int = 3,
    service_name: str = None,
    spec: Optional[Dict] = None,
):
    if service_name is None:
        service_name = resource_name + "-svc"

    if spec is None:
        spec = dict()

    pod_fqdn_fstring = "{resource_name}-{index}.{service_name}.{namespace}.svc.cluster.local".format(
        resource_name=resource_name,
        service_name=service_name,
        namespace=namespace,
        index="{}",
    )
    data = {}
    for idx in range(replicas):
        pod_dns = pod_fqdn_fstring.format(idx)
        pod_name = f"{resource_name}-{idx}"
        cert_secret_name = generate_cert(namespace, pod_name, pod_dns, issuer, spec)
        secret = read_secret(namespace, cert_secret_name)
        data[pod_name + "-pem"] = secret["tls.key"] + secret["tls.crt"]

    create_secret(namespace, bundle_secret_name, data)
    return bundle_secret_name


def approve_certificate(name: str) -> None:
    """Approves the CertificateSigningRequest with the provided name"""
    body = client.CertificatesV1beta1Api().read_certificate_signing_request_status(name)
    conditions = client.V1beta1CertificateSigningRequestCondition(
        last_update_time=datetime.now(timezone.utc).astimezone(),
        message="This certificate was approved by E2E testing framework",
        reason="E2ETestingFramework",
        type="Approved",
    )

    body.status.conditions = [conditions]
    client.CertificatesV1beta1Api().replace_certificate_signing_request_approval(
        name, body
    )


def yield_existing_csrs(
    csr_names: List[str], timeout: int = 300
) -> Generator[str, None, None]:
    """Returns certificate names as they start appearing in the Kubernetes API."""
    csr_names = csr_names.copy()
    total_csrs = len(csr_names)
    seen_csrs = 0
    stop_time = time.time() + timeout

    while len(csr_names) > 0 and time.time() < stop_time:
        csr = random.choice(csr_names)
        try:
            client.CertificatesV1beta1Api().read_certificate_signing_request_status(csr)
        except ApiException:
            time.sleep(3)
            continue

        seen_csrs += 1
        csr_names.remove(csr)
        yield csr

    if len(csr_names) == 0:
        # All the certificates have been "consumed" and yielded back to the user.
        return

    # we didn't find all of the expected csrs after the timeout period
    raise AssertionError(
        f"Expected to find {total_csrs} csrs, but only found {seen_csrs} after {timeout} seconds. Expected csrs {csr_names}"
    )
