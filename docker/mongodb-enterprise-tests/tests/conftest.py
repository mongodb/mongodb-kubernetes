import json
import logging
import os
import subprocess
import tempfile
import time
from typing import Any, Callable, Dict, List, Optional

import kubernetes
import requests
from kubernetes import client
from kubernetes.client import ApiextensionsV1Api
from kubetester import (
    create_or_update_configmap,
    get_deployments,
    get_pod_when_ready,
    is_pod_ready,
    read_secret,
    update_configmap,
)
from kubetester.awss3client import AwsS3Client
from kubetester.certs import (
    Certificate,
    ClusterIssuer,
    Issuer,
    create_mongodb_tls_certs,
    create_multi_cluster_mongodb_tls_certs,
)
from kubetester.helm import helm_install_from_chart
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as _fixture
from kubetester.kubetester import running_locally
from kubetester.mongodb_multi import MultiClusterClient
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from pymongo.errors import ServerSelectionTimeoutError
from pytest import fixture
from tests import test_logger
from tests.multicluster import prepare_multi_cluster_namespaces

try:
    kubernetes.config.load_kube_config()
except Exception:
    kubernetes.config.load_incluster_config()

AWS_REGION = "us-east-1"

KUBECONFIG_FILEPATH = "/etc/config/kubeconfig"
MULTI_CLUSTER_CONFIG_DIR = "/etc/multicluster"
# AppDB monitoring is disabled by default for e2e tests.
# If monitoring is needed use monitored_appdb_operator_installation_config / operator_with_monitored_appdb
MONITOR_APPDB_E2E_DEFAULT = "true"
MULTI_CLUSTER_OPERATOR_NAME = "mongodb-enterprise-operator-multi-cluster"
CLUSTER_HOST_MAPPING = {
    "us-central1-c_central": "https://35.232.85.244",
    "us-east1-b_member-1a": "https://35.243.222.230",
    "us-east1-c_member-2a": "https://34.75.94.207",
    "us-west1-a_member-3a": "https://35.230.121.15",
}

LEGACY_CENTRAL_CLUSTER_NAME: str = "__default"
LEGACY_DEPLOYMENT_STATE_VERSION: str = "1.27.0"

logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def namespace() -> str:
    return get_namespace()


def get_namespace() -> str:
    return os.environ["NAMESPACE"]


@fixture(scope="module")
def version_id() -> str:
    return get_version_id()


def get_version_id():
    """
    Returns VERSION_ID if it has been defined, or "latest" otherwise.
    """
    if "OVERRIDE_VERSION_ID" in os.environ:
        return os.environ["OVERRIDE_VERSION_ID"]
    return os.environ.get("VERSION_ID", "latest")


@fixture(scope="module")
def operator_installation_config(namespace: str) -> Dict[str, str]:
    return get_operator_installation_config(namespace)


def get_operator_installation_config(namespace):
    """Returns the ConfigMap containing configuration data for the Operator to be created.
    Created in the single_e2e.sh"""
    config = KubernetesTester.read_configmap(namespace, "operator-installation-config")
    config["customEnvVars"] = f"OPS_MANAGER_MONITOR_APPDB={MONITOR_APPDB_E2E_DEFAULT}"
    if os.getenv("OM_DEBUG_HTTP") == "true":
        print("Adding OM_DEBUG_HTTP=true to operator_installation_config")
        config["customEnvVars"] += "\&OM_DEBUG_HTTP=true"

    if local_operator():
        config["operator.replicas"] = "0"
    return config


@fixture(scope="module")
def monitored_appdb_operator_installation_config(operator_installation_config: Dict[str, str]) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created
    and for the AppDB to be monitored.
    Created in the single_e2e.sh"""
    config = operator_installation_config
    config["customEnvVars"] = "OPS_MANAGER_MONITOR_APPDB=true"
    return config


def get_multi_cluster_operator_installation_config(namespace: str) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created.
    Created in the single_e2e.sh"""
    config = KubernetesTester.read_configmap(
        namespace,
        "operator-installation-config",
        api_client=get_central_cluster_client(),
    )
    config["customEnvVars"] = f"OPS_MANAGER_MONITOR_APPDB={MONITOR_APPDB_E2E_DEFAULT}"
    return config


@fixture(scope="module")
def multi_cluster_operator_installation_config(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> Dict[str, str]:
    return get_multi_cluster_operator_installation_config(namespace)


@fixture(scope="module")
def multi_cluster_monitored_appdb_operator_installation_config(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    multi_cluster_operator_installation_config: dict[str, str],
) -> Dict[str, str]:
    multi_cluster_operator_installation_config["customEnvVars"] = f"OPS_MANAGER_MONITOR_APPDB=true"
    return multi_cluster_operator_installation_config


@fixture(scope="module")
def operator_clusterwide(
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    return get_operator_clusterwide(namespace, operator_installation_config)


def get_operator_clusterwide(namespace, operator_installation_config):
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
def operator_installation_config_quick_recovery(operator_installation_config: Dict[str, str]) -> Dict[str, str]:
    """
    This functions appends automatic recovery settings for CLOUDP-189433. In order to make the test runnable in
    reasonable time, we override the Recovery back off to 120 seconds. This gives enough time for the initial
    automation config to be published and statefulsets to be created before forcing the recovery.
    """
    operator_installation_config["customEnvVars"] = (
        operator_installation_config["customEnvVars"] + "\&MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S=120"
    )
    return operator_installation_config


@fixture(scope="module")
def evergreen_task_id() -> str:
    return get_evergreen_task_id()


def get_evergreen_task_id():
    taskId = os.environ.get("TASK_ID", "")
    return taskId


@fixture(scope="module")
def managed_security_context() -> str:
    return os.environ.get("MANAGED_SECURITY_CONTEXT", "False")


@fixture(scope="module")
def aws_s3_client(evergreen_task_id: str) -> AwsS3Client:
    return get_aws_s3_client(evergreen_task_id)


def get_aws_s3_client(evergreen_task_id: str = ""):
    tags = {"environment": "mongodb-enterprise-operator-tests"}

    if evergreen_task_id != "":
        tags["evg_task"] = evergreen_task_id

    return AwsS3Client("us-east-1", **tags)


@fixture(scope="session")
def crd_api():
    return ApiextensionsV1Api()


@fixture(scope="module")
def cert_manager() -> str:
    result = install_cert_manager(
        cluster_client=get_central_cluster_client(),
        cluster_name=get_central_cluster_name(),
    )
    wait_for_cert_manager_ready(cluster_client=get_central_cluster_client())
    return result


@fixture(scope="module")
def issuer(cert_manager: str, namespace: str) -> str:
    return create_issuer(namespace=namespace, api_client=get_central_cluster_client())


@fixture(scope="module")
def intermediate_issuer(cert_manager: str, issuer: str, namespace: str) -> str:
    """
    This fixture creates an intermediate "Issuer" in the testing namespace
    """
    # Create the Certificate for the intermediate CA based on the issuer fixture
    intermediate_ca_cert = Certificate(namespace=namespace, name="intermediate-ca-issuer")
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
    cert_manager: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    return create_issuer(namespace, central_cluster_client)


@fixture(scope="module")
def multi_cluster_clusterissuer(
    cert_manager: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    return create_issuer(namespace, central_cluster_client, clusterwide=True)


@fixture(scope="module")
def issuer_ca_filepath():
    return get_issuer_ca_filepath()


def get_issuer_ca_filepath():
    return _fixture("ca-tls-full-chain.crt")


@fixture(scope="module")
def custom_logback_file_path():
    return _fixture("custom_logback.xml")


@fixture(scope="module")
def amazon_ca_1_filepath():
    return _fixture("amazon-ca-1.pem")


@fixture(scope="module")
def amazon_ca_2_filepath():
    return _fixture("amazon-ca-2.pem")


@fixture(scope="module")
def multi_cluster_issuer_ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    return create_issuer_ca_configmap(issuer_ca_filepath, namespace, api_client=central_cluster_client)


def create_issuer_ca_configmap(
    issuer_ca_filepath: str, namespace: str, name: str = "issuer-ca", api_client: kubernetes.client.ApiClient = None
):
    """This is the CA file which verifies the certificates signed by it."""
    ca = open(issuer_ca_filepath).read()
    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}
    create_or_update_configmap(namespace, name, data, api_client=api_client)
    return name


@fixture(scope="module")
def issuer_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """This is the CA file which verifies the certificates signed by it."""
    ca = open(issuer_ca_filepath).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}

    name = "issuer-ca"
    create_or_update_configmap(namespace, name, data)
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
    create_or_update_configmap(namespace, name, data)
    return name


@fixture(scope="module")
def app_db_issuer_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """
    This is the custom ca used with the AppDB hosts. This can be the same as the one used
    for OM but does not need to be the same.
    """
    ca = open(issuer_ca_filepath).read()

    name = "app-db-issuer-ca"
    create_or_update_configmap(namespace, name, {"ca-pem": ca})
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
    create_or_update_configmap(namespace, name, data)
    yield name


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
    Defaults to 5.0.14 (simplifies testing locally)"""
    return get_custom_mdb_version()


@fixture(scope="module")
def cluster_domain() -> str:
    return get_cluster_domain()


def get_custom_mdb_version():
    return os.getenv("CUSTOM_MDB_VERSION", "6.0.7")


def get_cluster_domain():
    return os.getenv("CLUSTER_DOMAIN", "cluster.local")


@fixture(scope="module")
def custom_mdb_prev_version() -> str:
    """Returns a CUSTOM_MDB_PREV_VERSION for Mongodb to be created/upgraded to for testing.
    Defaults to 5.0.1 (simplifies testing locally)"""
    return os.getenv("CUSTOM_MDB_PREV_VERSION", "5.0.1")


@fixture(scope="module")
def custom_appdb_version(custom_mdb_version: str) -> str:
    """Returns a CUSTOM_APPDB_VERSION for AppDB to be created/upgraded to for testing,
    defaults to custom_mdb_version() (in most cases we need to use the same version for MongoDB as for AppDB)
    """

    return get_custom_appdb_version(custom_mdb_version)


def get_custom_appdb_version(custom_mdb_version: str = get_custom_mdb_version()):
    return os.getenv("CUSTOM_APPDB_VERSION", f"{custom_mdb_version}-ent")


@fixture(scope="module")
def custom_version() -> str:
    """Returns a CUSTOM_OM_VERSION for OM.
    Defaults to 5.0+ (for development)"""
    # The variable is set in context files with one of the values ops_manager_60_latest or ops_manager_70_latest
    # in .evergreen.yml
    return get_custom_om_version()


def get_custom_om_version():
    return os.getenv("CUSTOM_OM_VERSION", "6.0.22")


@fixture(scope="module")
def default_operator(
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    return get_default_operator(namespace, operator_installation_config)


def get_default_operator(
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    """Installs/upgrades a default Operator used by any test not interested in some custom Operator setting.
    TODO we use the helm template | kubectl apply -f process so far as Helm install/upgrade needs more refactoring in
    the shared environment"""
    operator = Operator(
        namespace=namespace,
        helm_args=operator_installation_config,
    ).upgrade()

    return operator


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


def get_central_cluster_name():
    central_cluster = LEGACY_CENTRAL_CLUSTER_NAME
    if is_multi_cluster():
        central_cluster = os.environ.get("CENTRAL_CLUSTER")
        if not central_cluster:
            raise ValueError("No central cluster specified in environment variable CENTRAL_CLUSTER!")
    return central_cluster


def is_multi_cluster():
    return len(os.getenv("MEMBER_CLUSTERS", "")) > 0


@fixture(scope="module")
def central_cluster_name() -> str:
    return get_central_cluster_name()


def get_central_cluster_client() -> kubernetes.client.ApiClient:
    if is_multi_cluster():
        return get_cluster_clients()[get_central_cluster_name()]
    else:
        return kubernetes.client.ApiClient()


@fixture(scope="module")
def central_cluster_client() -> kubernetes.client.ApiClient:
    return get_central_cluster_client()


def get_member_cluster_names() -> List[str]:
    if is_multi_cluster():
        member_clusters = os.environ.get("MEMBER_CLUSTERS")
        if not member_clusters:
            raise ValueError("No member clusters specified in environment variable MEMBER_CLUSTERS!")
        return sorted(member_clusters.split())
    else:
        return []


@fixture(scope="module")
def member_cluster_names() -> List[str]:
    return get_member_cluster_names()


def get_member_cluster_clients(cluster_mapping: dict[str, int] = None) -> List[MultiClusterClient]:
    if not is_multi_cluster():
        return [MultiClusterClient(kubernetes.client.ApiClient(), LEGACY_CENTRAL_CLUSTER_NAME)]

    member_cluster_clients = []
    for i, cluster_name in enumerate(sorted(get_member_cluster_names())):
        cluster_idx = i

        if cluster_mapping:
            cluster_idx = cluster_mapping[cluster_name]

        member_cluster_clients.append(
            MultiClusterClient(get_cluster_clients()[cluster_name], cluster_name, cluster_idx)
        )

    return member_cluster_clients


def get_member_cluster_client_map(deployment_state: dict[str, Any] = None) -> dict[str, MultiClusterClient]:
    return {
        multi_cluster_client.cluster_name: multi_cluster_client
        for multi_cluster_client in get_member_cluster_clients(deployment_state)
    }


def get_member_cluster_api_client(
    member_cluster_name: Optional[str],
) -> kubernetes.client.ApiClient:
    if is_member_cluster(member_cluster_name):
        return get_cluster_clients()[member_cluster_name]
    else:
        return kubernetes.client.ApiClient()


@fixture(scope="module")
def disable_istio(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    for mcc in member_cluster_clients:
        api = client.CoreV1Api(api_client=mcc.api_client)
        labels = {"istio-injection": "disabled"}
        ns = api.read_namespace(name=namespace)
        ns.metadata.labels.update(labels)
        api.replace_namespace(name=namespace, body=ns)
    return None


@fixture(scope="module")
def member_cluster_clients() -> List[MultiClusterClient]:
    return get_member_cluster_clients()


@fixture(scope="module")
def multi_cluster_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    return get_multi_cluster_operator(
        namespace,
        central_cluster_name,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        member_cluster_names,
    )


def get_multi_cluster_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name

    # when running with the local operator, this is executed by scripts/dev/prepare_local_e2e_run.sh
    if not local_operator():
        run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)
    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            # override the serviceAccountName for the operator deployment
            "operator.createOperatorServiceAccount": "false",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def multi_cluster_operator_with_monitored_appdb(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_monitored_appdb_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    print(f"\nSetting HELM_KUBECONTEXT to {central_cluster_name}")
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name

    # when running with the local operator, this is executed by scripts/dev/prepare_local_e2e_run.sh
    if not local_operator():
        run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)
    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_monitored_appdb_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            # override the serviceAccountName for the operator deployment
            "operator.createOperatorServiceAccount": "false",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def multi_cluster_operator_manual_remediation(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name
    if not local_operator():
        run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)
    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            # override the serviceAccountName for the operator deployment
            "operator.createOperatorServiceAccount": "false",
            "multiCluster.performFailOver": "false",
        },
        central_cluster_name,
    )


def get_multi_cluster_operator_clustermode(namespace: str) -> Operator:
    os.environ["HELM_KUBECONTEXT"] = get_central_cluster_name()
    run_kube_config_creation_tool(
        get_member_cluster_names(),
        namespace,
        namespace,
        get_member_cluster_names(),
        True,
    )
    return _install_multi_cluster_operator(
        namespace,
        get_multi_cluster_operator_installation_config(namespace),
        get_central_cluster_client(),
        get_member_cluster_clients(),
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            # override the serviceAccountName for the operator deployment
            "operator.createOperatorServiceAccount": "false",
            "operator.watchNamespace": "*",
        },
        get_central_cluster_name(),
    )


@fixture(scope="module")
def multi_cluster_operator_clustermode(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
    cluster_clients: Dict[str, kubernetes.client.ApiClient],
) -> Operator:
    return get_multi_cluster_operator_clustermode(namespace)


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
        mcn = ",".join(member_cluster_names)
        return _install_multi_cluster_operator(
            namespace,
            multi_cluster_operator_installation_config,
            central_cluster_client,
            member_cluster_clients,
            {
                "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
                # override the serviceAccountName for the operator deployment
                "operator.createOperatorServiceAccount": "false",
                "multiCluster.clusters": "{" + mcn + "}",
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
    operator_name: Optional[str] = MULTI_CLUSTER_OPERATOR_NAME,
    helm_chart_path: Optional[str] = "helm_chart",
    custom_operator_version: Optional[str] = None,
) -> Operator:
    prepare_multi_cluster_namespaces(
        namespace,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
        skip_central_cluster=True,
    )
    multi_cluster_operator_installation_config.update(helm_opts)

    operator = Operator(
        name=operator_name,
        namespace=namespace,
        helm_args=multi_cluster_operator_installation_config,
        api_client=central_cluster_client,
        helm_chart_path=helm_chart_path,
    ).upgrade(multi_cluster=True, custom_operator_version=custom_operator_version)

    # If we're running locally, then immediately after installing the deployment, we scale it to zero.
    # This way operator in POD is not interfering with locally running one.
    if local_operator():
        client.AppsV1Api(api_client=central_cluster_client).patch_namespaced_deployment_scale(
            namespace=namespace,
            name=operator.name,
            body={"spec": {"replicas": 0}},
        )

    return operator


@fixture(scope="module")
def official_operator(
    namespace: str,
    managed_security_context: str,
    operator_installation_config: Dict[str, str],
    central_cluster_name: str,
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    return install_official_operator(
        namespace,
        managed_security_context,
        operator_installation_config,
        central_cluster_name,
        central_cluster_client,
        member_cluster_clients,
        member_cluster_names,
        None,
    )


def install_official_operator(
    namespace: str,
    managed_security_context: str,
    operator_installation_config: Dict[str, str],
    central_cluster_name: Optional[str],
    central_cluster_client: Optional[client.ApiClient],
    member_cluster_clients: Optional[List[MultiClusterClient]],
    member_cluster_names: Optional[List[str]],
    custom_operator_version: Optional[str] = None,
) -> Operator:
    """
    Installs the Operator from the official Helm Chart.

    The version installed is always the latest version published as a Helm Chart.
    """
    logger.debug(
        f"Installing latest released {'multi' if is_multi_cluster() else 'single'} cluster operator from helm charts"
    )

    # When running in Openshift "managedSecurityContext" will be true.
    # When running in kind "managedSecurityContext" will be false, but still use the ubi images.
    helm_args = {
        "registry.imagePullSecrets": operator_installation_config["registry.imagePullSecrets"],
        "managedSecurityContext": managed_security_context,
        "operator.mdbDefaultArchitecture": operator_installation_config["operator.mdbDefaultArchitecture"],
    }
    name = "mongodb-enterprise-operator"

    # Note, that we don't intend to install the official Operator to standalone clusters (kops/openshift) as we want to
    # avoid damaged CRDs. But we may need to install the "openshift like" environment to Kind instead of the "ubi"
    # images are used for installing the dev Operator
    helm_args["operator.operator_image_name"] = "{}-ubi".format(name)

    # Note:
    # We might want in the future to install CRDs when performing upgrade/downgrade tests, the helm install only takes
    # care of the operator deployment.
    # A solution is to clone and checkout our helm_charts repository, and apply the CRDs from the right branch
    # Leaving below a code snippet to check out the right branch
    """
    # Version stored in env variable has format "1.27.0", tag name has format "enterprise-operator-1.27.0"
    if custom_operator_version:
        checkout_branch = f"enterprise-operator-{custom_operator_version}"
    else:
        checkout_branch = "main"

    temp_dir = tempfile.mkdtemp()
    # Values files are now located in `helm-charts` repo.
    clone_and_checkout(
        "https://github.com/mongodb/helm-charts",
        temp_dir,
        checkout_branch,  # branch or tag to check out from helm-charts.
    )
    """

    if is_multi_cluster():
        os.environ["HELM_KUBECONTEXT"] = central_cluster_name
        # when running with the local operator, this is executed by scripts/dev/prepare_local_e2e_run.sh
        if not local_operator():
            run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)
        helm_args.update(
            {
                "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
                # override the serviceAccountName for the operator deployment
                "operator.createOperatorServiceAccount": "false",
                "multiCluster.clusters": operator_installation_config["multiCluster.clusters"],
            }
        )
        # The "official" Operator will be installed, from the Helm Repo ("mongodb/enterprise-operator")
        # We pass helm_args as operator installation config below instead of the full configmap data, otherwise
        # it overwrites registries and image versions, and we wouldn't use the official images but the dev ones
        return _install_multi_cluster_operator(
            namespace,
            helm_args,
            central_cluster_client,
            member_cluster_clients,
            helm_opts=helm_args,
            central_cluster_name=get_central_cluster_name(),
            helm_chart_path="mongodb/enterprise-operator",
            custom_operator_version=custom_operator_version,
        )
    else:
        # When testing the UBI image type we need to assume a few things
        # The "official" Operator will be installed, from the Helm Repo ("mongodb/enterprise-operator")
        return Operator(
            namespace=namespace,
            helm_args=helm_args,
            helm_chart_path="mongodb/enterprise-operator",
            name=name,
        ).install(custom_operator_version=custom_operator_version)


# Function dumping the list of deployments and all their container images in logs.
# This is useful for example to ensure we are installing the correct operator version.
def log_deployments_info(namespace: str):
    logger.debug(f"Dumping deployments list and container images in namespace {namespace}:")
    logger.debug(log_deployment_and_images(get_deployments(namespace)))


def log_deployment_and_images(deployments):
    images, deployment_names = extract_container_images_and_deployments(deployments)
    for deployment in deployment_names:
        logger.debug(f"Deployment {deployment} contains images {images.get(deployment, 'error_getting_key')}")


# Extract container images and deployments names from the nested dict returned by kubetester
# Handles any missing key gracefully
def extract_container_images_and_deployments(deployments) -> (Dict[str, str], List[str]):
    deployment_images = {}
    deployment_names = []
    deployments = deployments.to_dict()

    if "items" not in deployments:
        logger.debug("Error: 'items' field not found in the response.")
        return deployment_images

    for deployment in deployments.get("items", []):
        try:
            deployment_name = deployment["metadata"].get("name", "Unknown")
            deployment_names.append(deployment_name)
            containers = deployment["spec"]["template"]["spec"].get("containers", [])

            # Extract images used by each container in the deployment
            images = [container.get("image", "No Image Specified") for container in containers]

            # Store it in a dictionary, to be logged outside of this function
            deployment_images[deployment_name] = images

        except KeyError as e:
            logger.debug(
                f"KeyError: Missing expected key in deployment {deployment.get('metadata', {}).get('name', 'Unknown')} - {e}"
            )
        except Exception as e:
            logger.debug(
                f"Error: An unexpected error occurred for deployment {deployment.get('metadata', {}).get('name', 'Unknown')} - {e}"
            )

    return deployment_images, deployment_names


def setup_agent_config(agent, with_process_support):
    log_rotate_config_for_process = {
        "sizeThresholdMB": "100",
        "percentOfDiskspace": "0.4",
        "numTotal": 10,
        "timeThresholdHrs": 1,
        "numUncompressed": 2,
    }

    log_rotate_for_backup_monitoring = {"sizeThresholdMB": 100, "timeThresholdHrs": 10}

    agent["backupAgent"] = {}
    agent["monitoringAgent"] = {}

    if with_process_support:
        agent["mongod"] = {}
        agent["mongod"]["logRotate"] = log_rotate_config_for_process
        agent["mongod"]["auditlogRotate"] = log_rotate_config_for_process

    agent["backupAgent"]["logRotate"] = log_rotate_for_backup_monitoring
    agent["monitoringAgent"]["logRotate"] = log_rotate_for_backup_monitoring


def setup_log_rotate_for_agents(resource, with_process_support=True):
    if "agent" not in resource["spec"] or resource["spec"]["agent"] is None:
        resource["spec"]["agent"] = {}
    setup_agent_config(resource["spec"]["agent"], with_process_support)


def assert_log_rotation_process(process, with_process_support=True):
    if with_process_support:
        _assert_log_rotation_process(process, "logRotate")
        _assert_log_rotation_process(process, "auditLogRotate")


def _assert_log_rotation_process(process, key):
    assert process[key]["sizeThresholdMB"] == 100
    assert process[key]["timeThresholdHrs"] == 1
    assert process[key]["percentOfDiskspace"] == 0.4
    assert process[key]["numTotal"] == 10
    assert process[key]["numUncompressed"] == 2


def assert_log_rotation_backup_monitoring(agent_config):
    assert agent_config["logRotate"]["sizeThresholdMB"] == 100
    assert agent_config["logRotate"]["timeThresholdHrs"] == 10


def _read_multi_cluster_config_value(value: str) -> str:
    multi_cluster_config_dir = os.environ.get("MULTI_CLUSTER_CONFIG_DIR", MULTI_CLUSTER_CONFIG_DIR)
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

    configuration = kubernetes.client.Configuration()
    kubernetes.config.load_kube_config(
        context=cluster_name,
        config_file=os.environ.get("KUBECONFIG", KUBECONFIG_FILEPATH),
        client_configuration=configuration,
    )
    configuration.host = CLUSTER_HOST_MAPPING.get(cluster_name, configuration.host)

    configuration.verify_ssl = False
    configuration.api_key = {"authorization": f"Bearer {token}"}
    return kubernetes.client.api_client.ApiClient(configuration=configuration)


def install_cert_manager(
    cluster_client: Optional[client.ApiClient] = None,
    cluster_name: Optional[str] = None,
    name="cert-manager",
    version="v1.5.4",
) -> str:
    if is_member_cluster(cluster_name):
        # ensure we cert-manager in the member clusters.
        os.environ["HELM_KUBECONTEXT"] = cluster_name

    install_required = True

    if running_locally():
        webhook_ready = is_pod_ready(
            name,
            f"app.kubernetes.io/instance={name},app.kubernetes.io/component=webhook",
            api_client=cluster_client,
        )
        controller_ready = is_pod_ready(
            name,
            f"app.kubernetes.io/instance={name},app.kubernetes.io/component=controller",
            api_client=cluster_client,
        )
        if webhook_ready is not None and controller_ready is not None:
            print("Cert manager already installed, skipping helm install")
            install_required = False

    if install_required:
        helm_install_from_chart(
            name,  # cert-manager is installed on a specific namespace
            name,
            f"jetstack/{name}",
            version=version,
            custom_repo=("jetstack", "https://charts.jetstack.io"),
            helm_args={"installCRDs": "true"},
        )

    return name


def wait_for_cert_manager_ready(
    cluster_client: Optional[client.ApiClient] = None,
    namespace="cert-manager",
):
    # waits until the cert-manager webhook and controller are Ready, otherwise creating
    # Certificate Custom Resources will fail.
    get_pod_when_ready(
        namespace,
        f"app.kubernetes.io/instance={namespace},app.kubernetes.io/component=webhook",
        api_client=cluster_client,
    )
    get_pod_when_ready(
        namespace,
        f"app.kubernetes.io/instance={namespace},app.kubernetes.io/component=controller",
        api_client=cluster_client,
    )


def get_cluster_clients() -> dict[str, kubernetes.client.api_client.ApiClient]:
    if not is_multi_cluster():
        return {
            LEGACY_CENTRAL_CLUSTER_NAME: kubernetes.client.ApiClient(),
        }

    member_clusters = [
        _read_multi_cluster_config_value("member_cluster_1"),
        _read_multi_cluster_config_value("member_cluster_2"),
    ]

    if len(get_member_cluster_names()) == 3:
        member_clusters.append(_read_multi_cluster_config_value("member_cluster_3"))
    return get_clients_for_clusters(member_clusters)


@fixture(scope="module")
def cluster_clients() -> dict[str, kubernetes.client.api_client.ApiClient]:
    return get_cluster_clients()


def get_clients_for_clusters(
    member_cluster_names: List[str],
) -> dict[str, kubernetes.client.ApiClient]:
    if not is_multi_cluster():
        return {
            LEGACY_CENTRAL_CLUSTER_NAME: kubernetes.client.ApiClient(),
        }

    central_cluster = _read_multi_cluster_config_value("central_cluster")

    return {c: _get_client_for_cluster(c) for c in ([central_cluster] + member_cluster_names)}


def get_api_servers_from_pod_kubeconfig(kubeconfig: str, cluster_clients: Dict[str, kubernetes.client.ApiClient]):
    api_servers = dict()
    fd, kubeconfig_tmp_path = tempfile.mkstemp()
    with os.fdopen(fd, "w") as fp:
        fp.write(kubeconfig)

        for cluster_name, cluster_client in cluster_clients.items():
            configuration = kubernetes.client.Configuration()
            kubernetes.config.load_kube_config(
                context=cluster_name,
                config_file=kubeconfig_tmp_path,
                client_configuration=configuration,
            )
            api_servers[cluster_name] = configuration.host

    return api_servers


def run_kube_config_creation_tool(
    member_clusters: List[str],
    central_namespace: str,
    member_namespace: str,
    member_cluster_names: List[str],
    cluster_scoped: Optional[bool] = False,
    service_account_name: Optional[str] = "mongodb-enterprise-operator-multi-cluster",
):
    central_cluster = _read_multi_cluster_config_value("central_cluster")
    member_clusters_str = ",".join(member_clusters)
    args = [
        os.getenv(
            "MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH",
            "multi-cluster-kube-config-creator",
        ),
        "multicluster",
        "setup",
        "--member-clusters",
        member_clusters_str,
        "--central-cluster",
        central_cluster,
        "--member-cluster-namespace",
        member_namespace,
        "--central-cluster-namespace",
        central_namespace,
        "--service-account",
        service_account_name,
    ]

    if os.getenv("MULTI_CLUSTER_CREATE_SERVICE_ACCOUNT_TOKEN_SECRETS") == "true":
        args.append("--create-service-account-secrets")

    if not local_operator():
        api_servers = get_api_servers_from_test_pod_kubeconfig(member_namespace, member_cluster_names)

        if len(api_servers) > 0:
            args.append("--member-clusters-api-servers")
            args.append(",".join([api_servers[member_cluster] for member_cluster in member_clusters]))

    if cluster_scoped:
        args.append("--cluster-scoped")

    try:
        print(f"Running multi-cluster cli setup tool: {' '.join(args)}")
        subprocess.check_output(args, stderr=subprocess.STDOUT)
        print("Finished running multi-cluster cli setup tool")
    except subprocess.CalledProcessError as exc:
        print(f"Status: FAIL Reason: {exc.output}")
        raise exc


def get_api_servers_from_kubeconfig_secret(
    namespace: str,
    secret_name: str,
    secret_cluster_client: kubernetes.client.ApiClient,
    cluster_clients: Dict[str, kubernetes.client.ApiClient],
):
    kubeconfig_secret = read_secret(namespace, secret_name, api_client=secret_cluster_client)
    return get_api_servers_from_pod_kubeconfig(kubeconfig_secret["kubeconfig"], cluster_clients)


def get_api_servers_from_test_pod_kubeconfig(namespace: str, member_cluster_names: List[str]) -> Dict[str, str]:
    test_pod_cluster = get_test_pod_cluster_name()
    cluster_clients = get_clients_for_clusters(member_cluster_names)

    return get_api_servers_from_kubeconfig_secret(
        namespace,
        "test-pod-kubeconfig",
        cluster_clients[test_pod_cluster],
        cluster_clients,
    )


def get_test_pod_cluster_name():
    return os.environ["TEST_POD_CLUSTER"]


def run_multi_cluster_recovery_tool(
    member_clusters: List[str],
    central_namespace: str,
    member_namespace: str,
    cluster_scoped: Optional[bool] = False,
) -> int:
    central_cluster = _read_multi_cluster_config_value("central_cluster")
    member_clusters_str = ",".join(member_clusters)
    args = [
        os.getenv(
            "MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH",
            "multi-cluster-kube-config-creator",
        ),
        "multicluster",
        "recover",
        "--member-clusters",
        member_clusters_str,
        "--central-cluster",
        central_cluster,
        "--member-cluster-namespace",
        member_namespace,
        "--central-cluster-namespace",
        central_namespace,
        "--operator-name",
        MULTI_CLUSTER_OPERATOR_NAME,
        "--source-cluster",
        member_clusters[0],
    ]
    if os.getenv("MULTI_CLUSTER_CREATE_SERVICE_ACCOUNT_TOKEN_SECRETS") == "true":
        args.append("--create-service-account-secrets")

    if cluster_scoped:
        args.extend(["--cluster-scoped", "true"])

    try:
        print(f"Running multi-cluster cli recovery tool: {' '.join(args)}")
        subprocess.check_output(args, stderr=subprocess.PIPE)
        print("Finished running multi-cluster cli recovery tool")
    except subprocess.CalledProcessError as exc:
        print("Status: FAIL", exc.returncode, exc.output)
        return exc.returncode
    return 0


def create_issuer(
    namespace: str,
    api_client: Optional[client.ApiClient] = None,
    clusterwide: bool = False,
):
    """
    This fixture creates an "Issuer" in the testing namespace. This requires cert-manager to be installed
    in the cluster. The ca-tls.key and ca-tls.crt are the private key and certificates used to generate
    certificates. This is based on a Cert-Manager CA Issuer.
    More info here: https://cert-manager.io/docs/configuration/ca/

    Please note, this cert will expire on Dec 8 07:53:14 2023 GMT.
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
        if clusterwide:
            client.CoreV1Api(api_client=api_client).create_namespaced_secret("cert-manager", secret)
        else:
            client.CoreV1Api(api_client=api_client).create_namespaced_secret(namespace, secret)
    except client.rest.ApiException as e:
        if e.status == 409:
            print("ca-key-pair already exists")
        else:
            raise e

    # And then creates the Issuer
    if clusterwide:
        issuer = ClusterIssuer(name="ca-issuer", namespace="")
    else:
        issuer = Issuer(name="ca-issuer", namespace=namespace)

    issuer["spec"] = {"ca": {"secretName": "ca-key-pair"}}
    issuer.api = kubernetes.client.CustomObjectsApi(api_client=api_client)

    try:
        issuer.create().block_until_ready()
    except client.rest.ApiException as e:
        if e.status == 409:
            print("issuer already exists")
        else:
            raise e

    return "ca-issuer"


def local_operator():
    """Checks if the current test run should assume that the operator is running locally, i.e. not in a pod."""
    return os.getenv("LOCAL_OPERATOR", "") == "true"


def pod_names(replica_set_name: str, replica_set_members: int) -> list[str]:
    """List of pod names for given replica set name."""
    return [f"{replica_set_name}-{i}" for i in range(0, replica_set_members)]


def multi_cluster_pod_names(replica_set_name: str, cluster_index_with_members: list[tuple[int, int]]) -> list[str]:
    """List of multi-cluster pod names for given replica set name and a list of member counts in member clusters."""
    result_list = []
    for cluster_index, members in cluster_index_with_members:
        result_list.extend([f"{replica_set_name}-{cluster_index}-{pod_idx}" for pod_idx in range(0, members)])

    return result_list


def multi_cluster_service_names(replica_set_name: str, cluster_index_with_members: list[tuple[int, int]]) -> list[str]:
    """List of multi-cluster service names for given replica set name and a list of member counts in member clusters."""
    return [f"{pod_name}-svc" for pod_name in multi_cluster_pod_names(replica_set_name, cluster_index_with_members)]


def is_member_cluster(cluster_name: Optional[str] = None) -> bool:
    if cluster_name is not None and cluster_name != LEGACY_CENTRAL_CLUSTER_NAME:
        return True
    return False


def default_external_domain() -> str:
    """Default external domain used for testing LoadBalancers on Kind."""
    return "mongodb.interconnected"


def external_domain_fqdns(
    replica_set_name: str,
    replica_set_members: int,
    external_domain: str = default_external_domain(),
) -> list[str]:
    """Builds list of hostnames for given replica set when connecting to it using external domain."""
    return [f"{pod_name}.{external_domain}" for pod_name in pod_names(replica_set_name, replica_set_members)]


def update_coredns_hosts(
    host_mappings: list[tuple[str, str]],
    cluster_name: Optional[str] = None,
    api_client: Optional[kubernetes.client.ApiClient] = None,
):
    """Updates kube-system/coredns config map with given host_mappings."""

    indent = " " * 7
    mapping_string = "\n".join([f"{indent}{host_mapping[0]} {host_mapping[1]}" for host_mapping in host_mappings])
    config_data = {"Corefile": coredns_config("interconnected", mapping_string)}

    if cluster_name is None:
        cluster_name = LEGACY_CENTRAL_CLUSTER_NAME

    print(f"Updating coredns for cluster: {cluster_name} with the following hosts list: {host_mappings}")
    update_configmap("kube-system", "coredns", config_data, api_client=api_client)


def coredns_config(tld: str, mappings: str):
    """Returns coredns config map data with mappings inserted."""
    return f"""
.:53 {{
    errors
    health {{
       lameduck 5s
    }}
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {{
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }}
    prometheus :9153
    forward . /etc/resolv.conf {{
       max_concurrent 1000
    }}
    cache 30
    loop
    reload
    loadbalance
    debug
    hosts /etc/coredns/customdomains.db   {tld} {{
{mappings}
       ttl 10
       reload 1m
       fallthrough
    }}
}}
"""


def create_appdb_certs(
    namespace: str,
    issuer: str,
    appdb_name: str,
    cluster_index_with_members: list[tuple[int, int]] = None,
    cert_prefix="appdb",
    clusterwide: bool = False,
) -> str:
    if cluster_index_with_members is None:
        cluster_index_with_members = [(0, 1), (1, 2)]

    appdb_cert_name = f"{cert_prefix}-{appdb_name}-cert"

    if is_multi_cluster():
        service_fqdns = [
            f"{svc}.{namespace}.svc.cluster.local"
            for svc in multi_cluster_service_names(appdb_name, cluster_index_with_members)
        ]
        create_multi_cluster_mongodb_tls_certs(
            issuer,
            appdb_cert_name,
            get_member_cluster_clients(),
            get_central_cluster_client(),
            service_fqdns=service_fqdns,
            namespace=namespace,
            clusterwide=clusterwide,
        )
    else:
        create_mongodb_tls_certs(issuer, namespace, appdb_name, appdb_cert_name, clusterwide=clusterwide)

    return cert_prefix


def pytest_sessionfinish(session, exitstatus):
    project_id = os.environ.get("OM_PROJECT_ID", "")
    if project_id:
        base_url = os.environ.get("OM_HOST")
        user = os.environ.get("OM_USER")
        key = os.environ.get("OM_API_KEY")
        ids = project_id.split(",")
        for project_id in ids:
            try:
                tester = OMTester(
                    OMContext(
                        base_url=base_url,
                        public_key=key,
                        project_id=project_id,
                        user=user,
                    )
                )

                # let's only access om if its healthy and around.
                status_code, _ = tester.request_health(base_url)
                if status_code == requests.status_codes.codes.OK:
                    ev = tester.get_project_events().json()["results"]
                    with open(f"/tmp/diagnostics/{project_id}-events.json", "w", encoding="utf-8") as f:
                        json.dump(ev, f, ensure_ascii=False, indent=4)
                else:
                    logging.info("om is not healthy - not collecting events information")

            except Exception as e:
                continue


def install_multi_cluster_operator_cluster_scoped(
    watch_namespaces: list[str],
    namespace: str = get_namespace(),
    central_cluster_name: str = get_central_cluster_name(),
    central_cluster_client: client.ApiClient = get_central_cluster_client(),
    multi_cluster_operator_installation_config: dict[str, str] = None,
    member_cluster_clients: list[kubernetes.client.ApiClient] = None,
    cluster_clients: dict[str, kubernetes.client.ApiClient] = None,
    member_cluster_names: list[str] = None,
) -> Operator:
    if multi_cluster_operator_installation_config is None:
        multi_cluster_operator_installation_config = get_multi_cluster_operator_installation_config(namespace).copy()
    if member_cluster_clients is None:
        member_cluster_clients = get_member_cluster_clients().copy()
    if cluster_clients is None:
        cluster_clients = get_cluster_clients().copy()
    if member_cluster_names is None:
        member_cluster_names = get_member_cluster_names().copy()

    print(
        f"Installing multi cluster operator in context: {central_cluster_name} and with watched namespaces: {watch_namespaces}"
    )
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name
    member_cluster_namespaces = ",".join(watch_namespaces)
    run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names, True)

    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            "operator.createOperatorServiceAccount": "false",
            "operator.watchNamespace": member_cluster_namespaces,
        },
        central_cluster_name,
    )


def assert_data_got_restored(test_data, collection1, collection2=None, timeout=300):
    """The data in the db has been restored to the initial state. Note, that this happens eventually - so
    we need to loop for some time (usually takes 60 seconds max). This is different from restoring from a
    specific snapshot (see the previous class) where the FINISHED restore job means the data has been restored.
    For PIT restores FINISHED just means the job has been created and the agents will perform restore eventually
    """
    print("\nWaiting until the db data is restored")
    start_time = time.time()
    last_error = None

    while True:
        elapsed_time = time.time() - start_time
        if elapsed_time > timeout:
            logger.debug("\nExisting data in MDB: {}".format(list(collection1.find())))
            if collection2 is not None:
                logger.debug("\nExisting data in MDB: {}".format(list(collection2.find())))
            raise AssertionError(
                f"The data hasn't been restored in {timeout // 60} minutes! Last assertion error was: {last_error}"
            )

        try:
            records = list(collection1.find())
            assert records == [test_data]

            if collection2 is not None:
                records = list(collection2.find())
                assert records == [test_data]

            return
        except AssertionError as e:
            logger.debug(f"assertionError while asserting data got restored: {e}")
            last_error = e
            pass
        except ServerSelectionTimeoutError:
            # The mongodb driver complains with `No replica set members
            # match selector "Primary()",` This could be related with DNS
            # not being functional, or the database going through a
            # re-election process. Let's give it another chance to succeed.
            logger.debug(f"ServerSelectionTimeoutError, are we going through a re-election?")
            pass
        except Exception as e:
            # We ignore Exception as there is usually a blip in connection (backup restore
            # results in reelection or whatever)
            # "Connection reset by peer" or "not master and slaveOk=false"
            logger.error("Exception happened while waiting for db data restore: ", e)
            # this is definitely the sign of a problem - no need continuing as each connection times out
            # after many minutes
            if "Connection refused" in str(e):
                raise e

        time.sleep(1)  # Sleep for a short duration before the next check


def verify_pvc_expanded(
    first_data_pvc_name,
    first_journal_pvc_name,
    first_logs_pvc_name,
    namespace,
    resized_storage_size,
    initial_storage_size,
):
    data_pvc = client.CoreV1Api().read_namespaced_persistent_volume_claim(first_data_pvc_name, namespace)
    assert data_pvc.status.capacity["storage"] == resized_storage_size
    journal_pvc = client.CoreV1Api().read_namespaced_persistent_volume_claim(first_journal_pvc_name, namespace)
    assert journal_pvc.status.capacity["storage"] == resized_storage_size
    logs_pvc = client.CoreV1Api().read_namespaced_persistent_volume_claim(first_logs_pvc_name, namespace)
    assert logs_pvc.status.capacity["storage"] == initial_storage_size
