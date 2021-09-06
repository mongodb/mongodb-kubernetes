import logging
import os
import subprocess
import tempfile
from typing import Optional, Dict, List

from kubernetes import client
from urllib3.util.retry import Retry
from requests.adapters import HTTPAdapter

import kubernetes
import requests
from kubernetes.client import ApiextensionsV1beta1Api
from kubetester import get_pod_when_ready, create_configmap
from kubetester.awss3client import AwsS3Client
from kubetester.certs import Issuer
from kubetester.git import clone_and_checkout
from kubetester.helm import helm_install_from_chart
from kubetester.kubetester import KubernetesTester, fixture as _fixture
from kubetester.operator import Operator
from pytest import fixture
from tests.multicluster import prepare_multi_cluster_namespaces
from kubetester import create_secret
from kubetester.mongodb_multi import MultiClusterClient

try:
    kubernetes.config.load_kube_config()
except Exception:
    kubernetes.config.load_incluster_config()


KUBECONFIG_FILEPATH = "/etc/config/kubeconfig"
MULTI_CLUSTER_CONFIG_DIR = "/etc/multicluster"
# AppDB monitoring is disabled by default for e2e tests.
# If monitoring is needed use monitored_appdb_operator_installation_config / operator_with_monitored_appdb
MONITOR_APPDB_E2E_DEFAULT = "false"
MULTI_CLUSTER_OPERATOR_NAME = "mongodb-enterprise-operator-multi-cluster"


@fixture(scope="module")
def namespace() -> str:
    return os.environ["PROJECT_NAMESPACE"]


@fixture(scope="module")
def operator_installation_config(namespace: str) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created.
    Created in the single_e2e.sh"""
    config = KubernetesTester.read_configmap(namespace, "operator-installation-config")
    config["customEnvVars"] = f"OPS_MANAGER_MONITOR_APPDB={MONITOR_APPDB_E2E_DEFAULT}"
    return config


@fixture(scope="module")
def monitored_appdb_operator_installation_config(namespace: str) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created
    and for the AppDB to be monitored.
    Created in the single_e2e.sh"""
    return KubernetesTester.read_configmap(namespace, "operator-installation-config")


@fixture(scope="module")
def multi_cluster_operator_installation_config(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created.
    Created in the single_e2e.sh"""
    config = KubernetesTester.read_configmap(
        namespace, "operator-installation-config", api_client=central_cluster_client
    )
    config["customEnvVars"] = f"OPS_MANAGER_MONITOR_APPDB={MONITOR_APPDB_E2E_DEFAULT}"
    return config


@fixture(scope="module")
def operator_clusterwide(
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    helm_args = operator_installation_config.copy()
    helm_args["operator.watchNamespace"] = "*"
    return Operator(namespace=namespace, helm_args=helm_args).install()


@fixture(scope="module")
def evergreen_task_id() -> str:
    return os.environ["TASK_ID"]


@fixture(scope="module")
def image_type() -> str:
    return os.environ["IMAGE_TYPE"]


@fixture(scope="module")
def managed_security_context() -> str:
    return os.environ["MANAGED_SECURITY_CONTEXT"]


@fixture(scope="module")
def custom_operator_release_version() -> Optional[str]:
    return os.environ.get("CUSTOM_OPERATOR_RELEASE_VERSION")


@fixture(scope="module")
def aws_s3_client() -> AwsS3Client:
    return AwsS3Client("us-east-1")


@fixture(scope="session")
def crd_api():
    return ApiextensionsV1beta1Api()


@fixture(scope="module")
def cert_manager(namespace: str) -> str:
    """Installs cert-manager v0.15.2 using Helm."""
    return install_cert_manager(namespace)


@fixture(scope="module")
def multi_cluster_cert_manager(
    namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    for client in member_cluster_clients:
        install_cert_manager(
            namespace,
            cluster_client=client.api_client,
            cluster_name=client.cluster_name,
        )


@fixture(scope="module")
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


@fixture(scope="module")
def multi_cluster_issuer(
    multi_cluster_cert_manager: str,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> str:
    """
    This fixture creates an "Issuer" in the testing namespace. This requires cert-manager
    to be installed in the cluster.
    The ca-tls.key and ca-tls.crt are the private key and certificates used to generate
    certificates. This is based on a Cert-Manager CA Issuer.
    More info here: https://cert-manager.io/docs/configuration/ca/
    """
    issuer_data = {
        "tls.key": open(_fixture("ca-tls.key")).read(),
        "tls.crt": open(_fixture("ca-tls.crt")).read(),
    }

    for client in member_cluster_clients:
        create_secret(
            namespace=namespace,
            name="ca-key-pair",
            data=issuer_data,
            api_client=client.api_client,
        )

        issuer = Issuer(name="ca-issuer", namespace=namespace)
        issuer["spec"] = {"ca": {"secretName": "ca-key-pair"}}
        issuer.api = kubernetes.client.CustomObjectsApi(api_client=client.api_client)

        issuer.create().block_until_ready()

    return "ca-issuer"


@fixture(scope="module")
def multi_cluster_issuer_ca_configmap(
    namespace: str, member_cluster_clients: List[MultiClusterClient]
) -> str:
    """This is the CA file which verifies the certificates signed by it."""
    ca = open(_fixture("ca-tls.crt")).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}

    name = "issuer-ca"

    for c in member_cluster_clients:
        create_configmap(namespace, name, data, api_client=c.api_client)
    return name


@fixture(scope="module")
def issuer_ca_configmap(namespace: str) -> str:
    """This is the CA file which verifies the certificates signed by it."""
    ca = open(_fixture("ca-tls.crt")).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}

    name = "issuer-ca"
    KubernetesTester.create_configmap(namespace, name, data)
    return name


@fixture(scope="module")
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


@fixture(scope="module")
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
    return os.getenv("CUSTOM_OM_VERSION", "5.0.2")


@fixture(scope="module")
def default_operator(
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    """Installs/upgrades a default Operator used by any test not interested in some custom Operator setting.
    TODO we use the helm template | kubectl apply -f process so far as Helm install/upgrade needs more refactoring in
    the shared environment"""
    return Operator(
        namespace=namespace,
        helm_args=operator_installation_config,
    ).upgrade(install=True)


@fixture(scope="module")
def operator_with_monitored_appdb(
    namespace: str,
    monitored_appdb_operator_installation_config: Dict[str, str],
) -> Operator:
    """Installs/upgrades a default Operator used by any test that needs the AppDB monitoring enabled."""
    return Operator(
        namespace=namespace,
        helm_args=monitored_appdb_operator_installation_config,
    ).upgrade(install=True)


@fixture(scope="module")
def central_cluster_name() -> str:
    central_cluster = os.environ.get("CENTRAL_CLUSTER")
    if not central_cluster:
        raise ValueError(
            "No central cluster specified in environment variable CENTRAL_CLUSTER!"
        )
    return central_cluster


@fixture(scope="module")
def central_cluster_client(
    central_cluster_name: str, cluster_clients: Dict[str, kubernetes.client.ApiClient]
) -> kubernetes.client.ApiClient:
    return cluster_clients[central_cluster_name]


@fixture(scope="module")
def member_cluster_clients(
    cluster_clients: Dict[str, kubernetes.client.ApiClient]
) -> List[MultiClusterClient]:
    member_clusters = os.environ.get("MEMBER_CLUSTERS")
    if not member_clusters:
        raise ValueError(
            "No member clusters specified in environment variable MEMBER_CLUSTERS!"
        )
    member_cluster_names = member_clusters.split()

    member_cluster_clients = []
    for (i, member_cluster) in enumerate(sorted(member_cluster_names)):
        member_cluster_clients.append(
            MultiClusterClient(cluster_clients[member_cluster], member_cluster, i)
        )
    return member_cluster_clients


@fixture(scope="module")
def multi_cluster_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
) -> Operator:
    prepare_multi_cluster_namespaces(
        namespace, multi_cluster_operator_installation_config, member_cluster_clients
    )
    # ensure we install the operator in the central cluster.
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name
    # override the serviceAccountName for the operator deployment
    multi_cluster_operator_installation_config[
        "operator.name"
    ] = MULTI_CLUSTER_OPERATOR_NAME
    multi_cluster_operator_installation_config[
        "operator.createOperatorServiceAccount"
    ] = "false"

    return Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        helm_args=multi_cluster_operator_installation_config,
        api_client=central_cluster_client,
        enable_webhook_check=False,
    ).upgrade(install=True)


@fixture(scope="module")
def operator_deployment_name(image_type: str) -> str:
    if image_type == "ubi":
        return "enterprise-operator"

    return "mongodb-enterprise-operator"


@fixture(scope="module")
def official_operator(
    namespace: str,
    image_type: str,
    managed_security_context: str,
    operator_installation_config: Dict[str, str],
    custom_operator_release_version: Optional[str],
) -> Operator:
    """Installs the Operator from the official GitHub repository. The version of the Operator is either passed to the
    function or the latest version is fetched from the repository.
    The configuration properties are not overridden - this can be added to the fixture parameters if necessary."""

    temp_dir = tempfile.mkdtemp()
    if custom_operator_release_version is None:
        custom_operator_release_version = fetch_latest_released_operator_version()

    enable_webhook_check = True
    logging.info("Updating from version {}".format(custom_operator_release_version))

    if custom_operator_release_version.startswith("1.10"):
        logging.info(
            "Will update from version < 1.11 with a broken webhook-service. "
            "We will not check webhook functionality during this test."
        )
        enable_webhook_check = False

    clone_and_checkout(
        "https://github.com/mongodb/mongodb-enterprise-kubernetes",
        temp_dir,
        custom_operator_release_version,
    )
    logging.info(
        "Checked out official Operator version {} to {}".format(
            custom_operator_release_version, temp_dir
        )
    )
    helm_options = []

    # When running in Openshift "managedSecurityContext" will be true.
    # When running in kind "managedSecurityContext" will be false, but still use the ubi images.

    helm_args = {
        "registry.imagePullSecrets": operator_installation_config[
            "registry.imagePullSecrets"
        ],
        "managedSecurityContext": managed_security_context,
    }
    name = "mongodb-enterprise-operator"

    # Note, that we don't intend to install the official Operator to standalone clusters (kops/openshift) as we want to
    # avoid damaged CRDs. But we may need to install the "openshift like" environment to Kind instead if the "ubi" images
    # are used for installing the dev Operator
    helm_args["operator.operator_image_name"] = name

    # When testing the UBI image type we need to assume a few things

    # 1. The testing cluster is Openshift
    # 2. The operator name is "enterprise-operator" (instead of "mongodb-enterprise-operator")
    # 3. The "values.yaml" file is "values-openshift.yaml"

    if image_type == "ubi":
        helm_options = [
            "--values",
            os.path.join(temp_dir, "helm_chart", "values-openshift.yaml"),
        ]
        helm_args["operator.operator_image_name"] = "enterprise-operator"

    return Operator(
        namespace=namespace,
        helm_args=helm_args,
        helm_chart_path=os.path.join(temp_dir, "helm_chart"),
        helm_options=helm_options,
        name=name,
        enable_webhook_check=enable_webhook_check,
    ).install()


def get_headers() -> Dict[str, str]:
    """
    Returns an authentication header that can be used when accessing
    the Github API. This is to avoid rate limiting when accessing the
    API from the Evergreen hosts.
    """

    if github_token := os.getenv("GITHUB_TOKEN_READ"):
        return {"Authorization": "token {}".format(github_token)}

    return dict()


def get_retriable_session() -> requests.Session:
    """
    Returns a request Session object with a retry mechanism.

    This is required to overcome a DNS resolution problem that we have
    experienced in the Evergreen hosts. This can also probably alleviate
    problems arising from request throttling.
    """

    s = requests.Session()
    retries = Retry(
        total=5,
        backoff_factor=2,
    )
    s.mount("https://", HTTPAdapter(max_retries=retries))

    return s


def fetch_latest_released_operator_version() -> str:
    """
    Fetches the currently released operator version from the Github API.
    """

    response = get_retriable_session().get(
        "https://api.github.com/repos/mongodb/mongodb-enterprise-kubernetes/releases/latest",
        headers=get_headers(),
    )
    response.raise_for_status()

    return response.json()["tag_name"]


def _read_multi_cluster_config_value(value: str) -> str:
    multi_cluster_config_dir = os.environ.get(
        "MULTI_CLUSTER_CONFIG_DIR", MULTI_CLUSTER_CONFIG_DIR
    )
    filepath = f"{multi_cluster_config_dir}/{value}".rstrip()
    if not os.path.isfile(filepath):
        raise ValueError(f"{filepath} does not exist!")
    with open(filepath, "r") as f:
        return f.read().strip()


def _get_client_for_cluster(
    cluster_name: str,
) -> kubernetes.client.api_client.ApiClient:
    token = _read_multi_cluster_config_value(cluster_name)
    if not token:
        raise ValueError(f"No token found for cluster {cluster_name}")

    kubernetes.config.load_kube_config(
        context=cluster_name,
        config_file=os.environ.get("KUBECONFIG", KUBECONFIG_FILEPATH),
    )
    configuration = kubernetes.client.Configuration()
    configuration.host = f"https://api.{cluster_name}"
    configuration.verify_ssl = False
    configuration.api_key = {"authorization": f"Bearer {token}"}
    return kubernetes.client.api_client.ApiClient(configuration=configuration)


def install_cert_manager(
    namespace: str,
    cluster_client: Optional[client.ApiClient] = None,
    cluster_name: Optional[str] = None,
    name="cert-manager",
    version="v0.15.2",
) -> str:

    if cluster_name is not None:
        # ensure we cert-manager in the member clusters.
        os.environ["HELM_KUBECONTEXT"] = cluster_name

    helm_install_from_chart(
        name,  # cert-manager is installed on a specific namespace
        name,
        f"jetstack/{name}",
        version=version,
        custom_repo=("jetstack", "https://charts.jetstack.io"),
        helm_args={"installCRDs": "true"},
    )

    # waits until the cert-manager webhook and controller are Ready, otherwise creating
    # Certificate Custom Resources will fail.
    get_pod_when_ready(
        name,
        f"app.kubernetes.io/instance={name},app.kubernetes.io/component=webhook",
        api_client=cluster_client,
    )
    get_pod_when_ready(
        name,
        f"app.kubernetes.io/instance={name},app.kubernetes.io/component=controller",
        api_client=cluster_client,
    )
    return name


@fixture(scope="module")
def cluster_clients(
    namespace: str,
) -> Dict[str, kubernetes.client.api_client.ApiClient]:
    central_cluster = _read_multi_cluster_config_value("central_cluster")
    member_clusters = [
        _read_multi_cluster_config_value("member_cluster_1"),
        _read_multi_cluster_config_value("member_cluster_2"),
        _read_multi_cluster_config_value("member_cluster_3"),
    ]
    member_clusters_str = ",".join(member_clusters)
    subprocess.call(
        [
            "multi-cluster-kube-config-creator",
            "-member-clusters",
            member_clusters_str,
            "-central-cluster",
            central_cluster,
            "-member-cluster-namespace",
            namespace,
            "-central-cluster-namespace",
            namespace,
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return {c: _get_client_for_cluster(c) for c in [central_cluster] + member_clusters}
