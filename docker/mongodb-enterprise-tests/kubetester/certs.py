"""
Certificate Custom Resource Definition.
"""

from kubeobject import CustomObject
import time

from kubetester.kubetester import KubernetesTester


ISSUER_CA_NAME = "ca-issuer"

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


def generate_cert(namespace, pod, pod_dns, issuer):
    cert = Certificate(namespace=namespace, name=pod)
    cert["spec"] = {
        "dnsNames": [pod, pod_dns],
        "secretName": pod,
        "issuerRef": {"name": issuer},
        "duration": "240h",
        "renewBefore": "120h",
        "usages": ["server auth", "client auth"],
    }
    return cert.create().block_until_ready()


def create_tls_certs(
    issuer: str, namespace: str, resource_name: str, secret_name: str, replicas: int = 3
):
    pod_fqdn_fstring = "{resource_name}-{index}.{resource_name}-svc.{namespace}.svc.cluster.local".format(
        resource_name=resource_name, namespace=namespace, index="{}",
    )
    data = {}
    for idx in range(replicas):
        pod_dns = pod_fqdn_fstring.format(idx)
        pod_name = f"{resource_name}-{idx}"
        generate_cert(namespace, pod_name, pod_dns, issuer)
        secret = KubernetesTester.read_secret(namespace, pod_name)
        data[pod_name + "-pem"] = secret["tls.key"] + secret["tls.crt"]
    KubernetesTester.create_secret(namespace, secret_name, data)
    return secret_name
