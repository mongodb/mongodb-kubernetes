import os
import subprocess
import tempfile
from typing import Callable, Dict, List, Optional

import kubernetes
from kubernetes import client
from kubernetes.client import ApiextensionsV1Api
from kubetester import create_configmap, create_secret, get_pod_when_ready
from kubetester.awss3client import AwsS3Client
from kubetester.certs import Issuer, Certificate
from kubetester.git import clone_and_checkout
from kubetester.helm import helm_install_from_chart
from kubetester.http import get_retriable_https_session
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb_multi import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture
from tests.multicluster import prepare_multi_cluster_namespaces

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
CLUSTER_HOST_MAPPING = {
    "us-central1-c_central": "https://35.232.85.244",
    "us-east1-b_member-1a": "https://35.243.222.230",
    "us-east1-c_member-2a": "https://34.75.94.207",
    "us-west1-a_member-3a": "https://35.230.121.15",
}


@fixture(scope="module")
def namespace() -> str:
    return os.environ["PROJECT_NAMESPACE"]


@fixture(scope="module")
def version_id() -> str:
    """
    Returns VERSION_ID if it has been defined, or "latest" otherwise.
    """
    return os.environ.get("VERSION_ID", "latest")


@fixture(scope="module")
def operator_installation_config(namespace: str, version_id: str) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created.
    Created in the single_e2e.sh"""
    config = KubernetesTester.read_configmap(namespace, "operator-installation-config")
    config["customEnvVars"] = f"OPS_MANAGER_MONITOR_APPDB={MONITOR_APPDB_E2E_DEFAULT}"

    # if running on evergreen don't use the default image tag
    if version_id != "latest":
        config["database.version"] = version_id
        config["initAppDb.version"] = version_id
        config["initDatabase.version"] = version_id
        config["initOpsManager.version"] = version_id

    return config


@fixture(scope="module")
def monitored_appdb_operator_installation_config(
    operator_installation_config: Dict[str, str]
) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created
    and for the AppDB to be monitored.
    Created in the single_e2e.sh"""
    config = operator_installation_config
    config["customEnvVars"] = "OPS_MANAGER_MONITOR_APPDB=true"
    return config


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
def operator_vault_secret_backend(
    namespace: str,
    monitored_appdb_operator_installation_config: Dict[str, str],
) -> Operator:
    helm_args = monitored_appdb_operator_installation_config.copy()
    helm_args["operator.vaultSecretBackend.enabled"] = "true"
    return Operator(namespace=namespace, helm_args=helm_args).install()


@fixture(scope="module")
def operator_vault_secret_backend_tls(
    namespace: str,
    monitored_appdb_operator_installation_config: Dict[str, str],
) -> Operator:
    helm_args = monitored_appdb_operator_installation_config.copy()
    helm_args["operator.vaultSecretBackend.enabled"] = "true"
    helm_args["operator.vaultSecretBackend.tlsSecretRef"] = "vault-tls"
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
def aws_s3_client() -> AwsS3Client:
    return AwsS3Client("us-east-1")


@fixture(scope="session")
def crd_api():
    return ApiextensionsV1Api()


@fixture(scope="module")
def cert_manager(namespace: str) -> str:
    """Installs cert-manager v1.5.4 using Helm."""
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
    return create_issuer(cert_manager=cert_manager, namespace=namespace)


@fixture(scope="module")
def multi_cluster_ldap_issuer(
    cert_manager: str,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):

    member_cluster_one = member_cluster_clients[0]
    return create_issuer(cert_manager, namespace, member_cluster_one.api_client)


@fixture(scope="module")
def intermediate_issuer(cert_manager: str, issuer: str, namespace: str) -> str:
    """
    This fixture creates an intermediate "Issuer" in the testing namespace
    """
    # Create the Certificate for the intermediate CA based on the issuer fixture
    intermediate_ca_cert = Certificate(
        namespace=namespace, name="intermediate-ca-issuer"
    )
    intermediate_ca_cert["spec"] = {
        "isCA": True,
        "commonName": "intermediate-ca-issuer",
        "secretName": "intermediate-ca-secret",
        "issuerRef": {"name": issuer},
        "dnsNames": ["intermediate-ca.example.com"],
    }
    intermediate_ca_cert.create().block_until_ready()

    # Create the intermediate issuer
    issuer = Issuer(name="intermediate-ca-issuer", namespace=namespace)
    issuer["spec"] = {"ca": {"secretName": "intermediate-ca-secret"}}
    issuer.create().block_until_ready()

    return "intermediate-ca-issuer"


@fixture(scope="module")
def multi_cluster_issuer(
    multi_cluster_cert_manager: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    return create_issuer(cert_manager, namespace, central_cluster_client)


@fixture(scope="module")
def issuer_ca_filepath():
    return _fixture("ca-tls-full-chain.crt")


@fixture(scope="module")
def multi_cluster_issuer_ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    """This is the CA file which verifies the certificates signed by it."""
    ca = open(issuer_ca_filepath).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}
    name = "issuer-ca"

    create_configmap(namespace, name, data, api_client=central_cluster_client)

    return name


@fixture(scope="module")
def issuer_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """This is the CA file which verifies the certificates signed by it."""
    ca = open(issuer_ca_filepath).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}

    name = "issuer-ca"
    KubernetesTester.create_configmap(namespace, name, data)
    return name


@fixture(scope="module")
def ops_manager_issuer_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """
    This is the CA file which verifies the certificates signed by it.
    This CA is used to community with Ops Manager. This is needed by the database pods
    which talk to OM.
    """
    ca = open(issuer_ca_filepath).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"mms-ca.crt": ca}

    name = "ops-manager-issuer-ca"
    KubernetesTester.create_configmap(namespace, name, data)
    return name


@fixture(scope="module")
def app_db_issuer_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """
    This is the custom ca used with the AppDB hosts. This can be the same as the one used
    for OM but does not need to be the same.
    """
    ca = open(issuer_ca_filepath).read()

    name = "app-db-issuer-ca"
    KubernetesTester.create_configmap(namespace, name, {"ca-pem": ca})
    return name


@fixture(scope="module")
def issuer_ca_plus(issuer_ca_filepath: str, namespace: str) -> str:
    """Returns the name of a ConfigMap which includes a custom CA and the full
    certificate chain for downloads.mongodb.com, fastdl.mongodb.org,
    downloads.mongodb.org. This allows for the use of a custom CA while still
    allowing the agent to download from MongoDB servers.

    """
    ca = open(issuer_ca_filepath).read()
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
    return os.getenv("CUSTOM_MDB_VERSION", "5.0.5")


@fixture(scope="module")
def custom_appdb_version(custom_mdb_version: str) -> str:
    """Returns a CUSTOM_APPDB_VERSION for AppDB to be created/upgraded to for testing,
    defaults to custom_mdb_version() (in most cases we need to use the same version for MongoDB as for AppDB)"""

    return os.getenv("CUSTOM_APPDB_VERSION", f"{custom_mdb_version}-ent")


@fixture(scope="module")
def custom_version() -> str:
    """Returns a CUSTOM_OM_VERSION for OM.
    Defaults to 5.0+ (for development)"""
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
    ).upgrade()


@fixture(scope="module")
def operator_with_monitored_appdb(
    namespace: str,
    monitored_appdb_operator_installation_config: Dict[str, str],
) -> Operator:
    """Installs/upgrades a default Operator used by any test that needs the AppDB monitoring enabled."""
    return Operator(
        namespace=namespace,
        helm_args=monitored_appdb_operator_installation_config,
    ).upgrade()


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
def member_cluster_names() -> List[str]:
    member_clusters = os.environ.get("MEMBER_CLUSTERS")
    if not member_clusters:
        raise ValueError(
            "No member clusters specified in environment variable MEMBER_CLUSTERS!"
        )
    return sorted(member_clusters.split())


@fixture(scope="module")
def member_cluster_clients(
    cluster_clients: Dict[str, kubernetes.client.ApiClient],
    member_cluster_names: List[str],
) -> List[MultiClusterClient]:
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
    member_cluster_names: List[str],
) -> Operator:
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name
    run_kube_config_creation_tool(member_cluster_names, namespace, namespace)
    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.deployment_name": MULTI_CLUSTER_OPERATOR_NAME,
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            # override the serviceAccountName for the operator deployment
            "operator.createOperatorServiceAccount": "false",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def multi_cluster_operator_clustermode(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name
    run_kube_config_creation_tool(member_cluster_names, namespace, namespace, True)
    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.deployment_name": MULTI_CLUSTER_OPERATOR_NAME,
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            # override the serviceAccountName for the operator deployment
            "operator.createOperatorServiceAccount": "false",
            "operator.watchNamespace": "*",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def install_multi_cluster_operator_set_members_fn(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
) -> Callable[[List[str]], Operator]:
    def _fn(member_cluster_names: List[str]) -> Operator:
        os.environ["HELM_KUBECONTEXT"] = central_cluster_name
        return _install_multi_cluster_operator(
            namespace,
            multi_cluster_operator_installation_config,
            central_cluster_client,
            member_cluster_clients,
            {
                "operator.deployment_name": MULTI_CLUSTER_OPERATOR_NAME,
                "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
                # override the serviceAccountName for the operator deployment
                "operator.createOperatorServiceAccount": "false",
                "multiCluster.clusters": ",".join(member_cluster_names),
            },
            central_cluster_name,
        )

    return _fn


def _install_multi_cluster_operator(
    namespace: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    helm_opts: Dict[str, str],
    central_cluster_name: str,
) -> Operator:
    prepare_multi_cluster_namespaces(
        namespace,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
    )
    multi_cluster_operator_installation_config.update(helm_opts)

    return Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        helm_args=multi_cluster_operator_installation_config,
        api_client=central_cluster_client,
    ).upgrade(multi_cluster=True)


@fixture(scope="module")
def operator_deployment_name(image_type: str, is_multi: bool = False) -> str:
    if is_multi:
        return MULTI_CLUSTER_OPERATOR_NAME

    if image_type == "ubi":
        return "enterprise-operator"

    return "mongodb-enterprise-operator"


@fixture(scope="module")
def official_operator(
    namespace: str,
    image_type: str,
    managed_security_context: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    """
    Installs the Operator from the official Helm Chart.

    The version installed is always the latest version published as a Helm Chart.
    """

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

    temp_dir = tempfile.mkdtemp()
    # Values files are now located in `helm-charts` repo.
    clone_and_checkout(
        "https://github.com/mongodb/helm-charts",
        temp_dir,
        "main",  # main branch of helm-charts.
    )
    chart_dir = os.path.join(temp_dir, "charts", "enterprise-operator")

    # When testing the UBI image type we need to assume a few things

    # 1. The testing cluster is Openshift
    # 2. The operator name is "enterprise-operator" (instead of "mongodb-enterprise-operator")
    # 3. The "values.yaml" file is "values-openshift.yaml"
    if image_type == "ubi":
        helm_options = [
            "--values",
            os.path.join(chart_dir, "values-openshift.yaml"),
        ]
        helm_args["operator.operator_image_name"] = "enterprise-operator"

    # The "official" Operator will be installed, from the Helm Repo ("mongodb/enterprise-operator")
    return Operator(
        namespace=namespace,
        helm_args=helm_args,
        helm_chart_path="mongodb/enterprise-operator",
        helm_options=helm_options,
        name=name,
    ).install()


@fixture(scope="module")
def official_operator_v12(
    namespace: str,
    image_type: str,
    managed_security_context: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    """
    Installs version 1.12 of the Operator
    """
    helm_options = []

    name = "mongodb-enterprise-operator"
    helm_args = {
        "registry.imagePullSecrets": operator_installation_config[
            "registry.imagePullSecrets"
        ],
        "managedSecurityContext": managed_security_context,
        "operator.operator_image_name": name,
    }

    temp_dir = tempfile.mkdtemp()
    # For Operator v1.12 the Helm chart resided in the same "public" repo.
    clone_and_checkout(
        "https://github.com/mongodb/mongodb-enterprise-kubernetes",
        temp_dir,
        "1.12.0",
    )

    chart_dir = os.path.join(temp_dir, "helm_chart")
    if image_type == "ubi":
        helm_options = [
            "--values",
            os.path.join(chart_dir, "values-openshift.yaml"),
        ]
        helm_args["operator.operator_image_name"] = "enterprise-operator"

    return Operator(
        namespace=namespace,
        helm_args=helm_args,
        helm_chart_path=chart_dir,
        helm_options=helm_options,
        name=name,
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


def fetch_latest_released_operator_version() -> str:
    """
    Fetches the currently released operator version from the Github API.
    """

    response = get_retriable_https_session(tls_verify=True).get(
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

    configuration.host = CLUSTER_HOST_MAPPING.get(
        cluster_name, f"https://api.{cluster_name}"
    )

    configuration.verify_ssl = False
    configuration.api_key = {"authorization": f"Bearer {token}"}
    return kubernetes.client.api_client.ApiClient(configuration=configuration)


def install_cert_manager(
    namespace: str,
    cluster_client: Optional[client.ApiClient] = None,
    cluster_name: Optional[str] = None,
    name="cert-manager",
    version="v1.5.4",
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
    namespace: str, member_cluster_names: List[str]
) -> Dict[str, kubernetes.client.api_client.ApiClient]:
    member_clusters = [
        _read_multi_cluster_config_value("member_cluster_1"),
        _read_multi_cluster_config_value("member_cluster_2"),
    ]

    if len(member_cluster_names) == 3:
        member_clusters.append(_read_multi_cluster_config_value("member_cluster_3"))
    return get_clients_for_clusters(member_clusters, namespace)


def get_clients_for_clusters(
    member_cluster_names: List[str], namespace: str
) -> Dict[str, kubernetes.client.ApiClient]:
    central_cluster = _read_multi_cluster_config_value("central_cluster")
    return {
        c: _get_client_for_cluster(c) for c in [central_cluster] + member_cluster_names
    }


def run_kube_config_creation_tool(
    member_clusters: List[str],
    central_namespace: str,
    member_namespace: str,
    cluster_scoped: Optional[bool] = False,
):
    central_cluster = _read_multi_cluster_config_value("central_cluster")
    member_clusters_str = ",".join(member_clusters)
    args = [
        "multi-cluster-kube-config-creator",
        "-member-clusters",
        member_clusters_str,
        "-central-cluster",
        central_cluster,
        "-member-cluster-namespace",
        member_namespace,
        "-central-cluster-namespace",
        central_namespace,
    ]
    if cluster_scoped:
        args.extend(["-cluster-scoped", "true"])

    subprocess.call(
        args,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )


def create_issuer(
    cert_manager: str, namespace: str, api_client: Optional[client.ApiClient] = None
):
    """
    This fixture creates an "Issuer" in the testing namespace. This requires cert-manager to be installed in the cluster.
    The ca-tls.key and ca-tls.crt are the private key and certificates used to generate
    certificates. This is based on a Cert-Manager CA Issuer.
    More info here: https://cert-manager.io/docs/configuration/ca/

    Please note, this cert will expire on Dec 11 15:54:21 2022 GMT.
    """
    issuer_data = {
        "tls.key": open(_fixture("ca-tls.key")).read(),
        "tls.crt": open(_fixture("ca-tls.crt")).read(),
    }
    secret = client.V1Secret(
        metadata=client.V1ObjectMeta(name="ca-key-pair"),
        string_data=issuer_data,
    )

    try:
        client.CoreV1Api(api_client=api_client).create_namespaced_secret(
            namespace, secret
        )
    except client.rest.ApiException as e:
        if e.status == 409:
            print("ca-key-pair already exists")

    # And then creates the Issuer
    issuer = Issuer(name="ca-issuer", namespace=namespace)
    issuer["spec"] = {"ca": {"secretName": "ca-key-pair"}}
    issuer.api = kubernetes.client.CustomObjectsApi(api_client=api_client)

    try:
        issuer.create().block_until_ready()
    except client.rest.ApiException as e:
        if e.status == 409:
            print("issuer already exists")

    return "ca-issuer"
