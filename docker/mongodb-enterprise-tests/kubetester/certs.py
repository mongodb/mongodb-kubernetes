"""
Certificate Custom Resource Definition.
"""

from kubeobject import CustomObject
import time


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
