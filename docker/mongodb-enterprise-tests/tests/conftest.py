import os
import tempfile
from typing import Optional, Dict

import kubernetes
import requests
from kubernetes.client import ApiextensionsV1beta1Api
from kubetester import get_pod_when_ready
from kubetester.awss3client import AwsS3Client
from kubetester.certs import Issuer
from kubetester.git import clone_and_checkout
from kubetester.helm import helm_install_from_chart
from kubetester.kubetester import KubernetesTester, fixture as _fixture
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
def operator_installation_config(namespace: str) -> Dict[str, str]:
    """ Returns the ConfigMap containing configuration data for the Operator to be created.
    Created in the single_e2e.sh """
    return KubernetesTester.read_configmap(namespace, "operator-installation-config")


@fixture(scope="module")
def evergreen_task_id() -> str:
    return get_env_variable_or_fail("TASK_ID")


@fixture(scope="module")
def custom_operator_release_version() -> Optional[str]:
    return os.environ.get("CUSTOM_OPERATOR_RELEASE_VERSION")


@fixture(scope="module")
def aws_s3_client() -> AwsS3Client:
    return AwsS3Client("us-east-1")


@fixture(scope="session")
def crd_api():
    return ApiextensionsV1beta1Api()


@fixture("module")
def cert_manager(namespace: str) -> str:
    """Installs cert-manager v0.15.2 using Helm."""
    name = "cert-manager"
    helm_install_from_chart(
        name,  # cert-manager is installed on a specific namespace
        name,
        f"jetstack/{name}",
        version="v0.15.2",
        custom_repo=("jetstack", "https://charts.jetstack.io"),
        helm_args={"installCRDs": "true"},
    )

    # waits until the cert-manager webhook is Ready, otherwise creating Certificate
    # Custom Resources will fail.
    get_pod_when_ready(
        name, f"app.kubernetes.io/instance={name},app.kubernetes.io/component=webhook",
    )

    return name


@fixture("module")
def issuer(cert_manager: str, namespace: str) -> str:
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

    name = "issuer-ca"
    KubernetesTester.create_configmap(namespace, name, data)
    yield name

    KubernetesTester.delete_configmap(namespace, name)


@fixture("module")
def issuer_ca_plus(namespace: str) -> str:
    """Returns the name of a ConfigMap which includes a custom CA and the full
    certificate chain for downloads.mongodb.com, fastdl.mongodb.org,
    downloads.mongodb.org. This allows for the use of a custom CA while still
    allowing the agent to download from MongoDB servers.

    """
    ca = open(_fixture("ca-tls.crt")).read()
    plus_ca = open(_fixture("downloads.mongodb.com.chained+root.crt")).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca + plus_ca, "mms-ca.crt": ca + plus_ca}

    name = "issuer-plus-ca"
    KubernetesTester.create_configmap(namespace, name, data)
    yield name

    KubernetesTester.delete_configmap(namespace, name)


@fixture("module")
def ca_path() -> str:
    """Returns a relative path to a file containing the CA.
    This is required to test TLS enabled connections to MongoDB like:

    def test_connect(replica_set: MongoDB, ca_path: str)
        replica_set.assert_connectivity(ca_path=ca_path)
    """
    return _fixture("ca-tls.crt")


@fixture(scope="module")
def custom_mdb_version() -> str:
    """Returns a CUSTOM_MDB_VERSION for Mongodb to be created/upgraded to for testing.
    Defaults to 4.4.0 (simplifies testing locally)"""
    return os.getenv("CUSTOM_MDB_VERSION", "4.4.0")


@fixture(scope="module")
def custom_version() -> str:
    """Returns a CUSTOM_OM_VERSION for OM.
    Defaults to 4.4+ (for development)"""
    return os.getenv("CUSTOM_OM_VERSION", "4.4.1")


@fixture("module")
def default_operator(
    namespace: str, operator_installation_config: Dict[str, str],
) -> Operator:
    """ Installs/upgrades a default Operator used by any test not interested in some custom Operator setting.
    TODO we use the helm template | kubectl apply -f process so far as Helm install/upgrade needs more refactoring in
    the shared environment"""
    return Operator(
        namespace=namespace, helm_args=operator_installation_config,
    ).upgrade(install=True)


@fixture("module")
def official_operator(
    namespace: str,
    operator_installation_config: Dict[str, str],
    custom_operator_release_version: Optional[str],
) -> Operator:
    """ Installs the Operator from the official GitHub repository. The version of the Operator is either passed to the
    function or the latest version is fetched from the repository.
    The configuration properties are not overridden - this can be added to the fixture parameters if necessary. """
    temp_dir = tempfile.mkdtemp()
    if custom_operator_release_version is None:
        custom_operator_release_version = fetch_latest_released_operator_version()

    clone_and_checkout(
        "https://github.com/mongodb/mongodb-enterprise-kubernetes",
        temp_dir,
        custom_operator_release_version,
    )
    print()
    print(
        "Checked out official Operator version {} to {}".format(
            custom_operator_release_version, temp_dir
        )
    )
    helm_options = []
    if operator_installation_config["managedSecurityContext"] == "true":
        # for openshift we need to use the non-default values file
        helm_options = [
            "--values",
            os.path.join(temp_dir, "helm_chart", "values-openshift.yaml"),
        ]

    return Operator(
        namespace=namespace,
        helm_chart_path=os.path.join(temp_dir, "helm_chart"),
        helm_options=helm_options,
    ).install()


def get_env_variable_or_fail(env_var_name: str) -> str:
    value = os.getenv(env_var_name, None)

    if value is None:
        raise ValueError(f"{env_var_name} needs to be defined")

    return value


def fetch_latest_released_operator_version() -> str:
    response = requests.request(
        "get",
        "https://api.github.com/repos/mongodb/mongodb-enterprise-kubernetes/releases/latest",
    )
    if response.status_code != 200:
        raise Exception(
            f"Failed to find out the latest Operator version, response: {response}"
        )

    return response.json()["tag_name"]
