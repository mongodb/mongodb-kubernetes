import os
import pathlib
from typing import List

import kubernetes
import pytest
from _pytest.nodes import Node
from kubernetes.client import ApiextensionsV1beta1Api

from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import KubernetesTester, fixture as _fixture
from kubetester.certs import Issuer
from kubetester.operator import Operator
from pytest import fixture

try:
    kubernetes.config.load_kube_config()
except Exception:
    kubernetes.config.load_incluster_config()


@fixture(scope="module")
def namespace() -> str:
    return get_env_variable_or_fail("PROJECT_NAMESPACE")


@fixture(scope="module")
def operator_version() -> str:
    return get_env_variable_or_fail("OPERATOR_VERSION")


@fixture(scope="module")
def operator_registry_url() -> str:
    return get_env_variable_or_fail("OPERATOR_REGISTRY_URL")


@fixture(scope="module")
def om_init_registry_url() -> str:
    return get_env_variable_or_fail("OPS_MANAGER_INIT_REGISTRY_URL")


@fixture(scope="module")
def appdb_init_registry_url() -> str:
    return get_env_variable_or_fail("APPDB_INIT_REGISTRY_URL")


@fixture(scope="module")
def om_registry_url() -> str:
    return get_env_variable_or_fail("OPS_MANAGER_REGISTRY_URL")


@fixture(scope="module")
def appdb_registry_url() -> str:
    return get_env_variable_or_fail("APPDB_REGISTRY_URL")


@fixture(scope="module")
def ops_manager_name() -> str:
    return get_env_variable_or_fail("OPS_MANAGER_NAME")


@fixture(scope="module")
def appdb_name() -> str:
    return get_env_variable_or_fail("APPDB_NAME")


@fixture(scope="module")
def managed_security_context() -> bool:
    return get_env_variable_or_fail("MANAGED_SECURITY_CONTEXT") == "true"


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


@fixture("module")
def ca_path() -> str:
    """Returns a relative path to a file containing the CA.
    This is required to test TLS enabled connections to MongoDB like:

    def test_connect(replica_set: MongoDB, ca_path: str)
        replica_set.assert_connectivity(ca_path=ca_path)
    """
    return _fixture("ca-tls.crt")


@fixture("module")
def default_operator(
    namespace: str,
    operator_version: str,
    operator_registry_url: str,
    om_init_registry_url: str,
    appdb_init_registry_url: str,
    om_registry_url: str,
    appdb_registry_url: str,
    ops_manager_name: str,
    appdb_name: str,
    managed_security_context: bool,
) -> Operator:
    """ Installs/upgrades a default Operator used by any test not interested in some custom Operator setting.
    TODO we use the helm template | kubectl apply -f process so far as Helm install/upgrade needs more refactoring in
    the shared environment"""
    return Operator(
        namespace=namespace,
        operator_version=operator_version,
        operator_registry_url=operator_registry_url,
        init_om_registry_url=om_init_registry_url,
        init_appdb_registry_url=appdb_init_registry_url,
        ops_manager_registry_url=om_registry_url,
        appdb_registry_url=appdb_registry_url,
        ops_manager_name=ops_manager_name,
        appdb_name=appdb_name,
        managed_security_context=managed_security_context,
    ).install()


def get_env_variable_or_fail(env_var_name: str) -> str:
    value = os.getenv(env_var_name, None)

    if value is None:
        raise ValueError(f"{env_var_name} needs to be defined")

    return value
