import os

import kubernetes
from kubernetes.client import ApiextensionsV1beta1Api

from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import KubernetesTester, fixture as _fixture
from kubetester.certs import Issuer
from pytest import fixture

try:
    kubernetes.config.load_kube_config()
except Exception:
    kubernetes.config.load_incluster_config()


@fixture(scope="module")
def namespace() -> str:
    namespace = os.getenv("PROJECT_NAMESPACE", None)

    if namespace is None:
        raise Exception("PROJECT_NAMESPACE needs to be defined")

    return namespace


@fixture(scope="module")
def aws_s3_client() -> AwsS3Client:
    return AwsS3Client("us-east-1")


@fixture(scope="session")
def crd_api():
    return ApiextensionsV1beta1Api()


@fixture("module")
def issuer(namespace: str) -> str:
    """
    This fixture creates an "Issuer" in the testing namespace. This requires cert-manager
    to be installed in the cluster.
    The ca-tls.key and ca-tls.crt are the private key and certificates used to generate
    certificates. This is based on a Cert-Manager CA Issuer.
    More info here: https://cert-manager.io/docs/configuration/ca/

    Please note, this cert will expire on Dec 11 15:54:21 2022 GMT.
    """
    issuer_data = {
        "tls.key": open(_fixture("ca-tls.key")).read(),
        "tls.crt": open(_fixture("ca-tls.crt")).read(),
    }
    KubernetesTester.create_secret(namespace, "ca-key-pair", issuer_data)

    # And then creates the Issuer
    issuer = Issuer(name="ca-issuer", namespace=namespace)
    issuer["spec"] = {"ca": {"secretName": "ca-key-pair"}}
    issuer.create().block_until_ready()

    return "ca-issuer"


@fixture("module")
def issuer_ca_configmap(namespace: str) -> str:
    """This is the CA file which verifies the certificates signed by it."""
    ca = open(_fixture("ca-tls.crt")).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}
    KubernetesTester.create_configmap(namespace, "issuer-ca", data)

    return "issuer-ca"
