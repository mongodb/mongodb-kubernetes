import base64
import json

from kubernetes import client
from kubetester import create_or_update_secret, pod_is_ready, try_load
from kubetester.certs import create_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.connectivity import wait_for_all_pods_replaced, wait_for_resource_deleted
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.tls_utils import create_keyfile_password_secret, encrypt_tls_key_with_password
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBC_RESOURCE_NAME = "mdbc-rs"
# MongoDBSearch uses the same name as MDBC (from search-minimal.yaml default)
MDBS_RESOURCE_NAME = MDBC_RESOURCE_NAME

TLS_SECRET_NAME = "tls-secret"

# MongoDBSearch TLS configuration -- convention: {name}-search-cert
MDBS_TLS_SECRET_NAME = search_resource_names.mongot_tls_cert_name(MDBS_RESOURCE_NAME)
GRPC_KEY_PASSWORD = "search-grpc-key-password"
GRPC_KEY_PASSWORD_SECRET_NAME = f"{MDBS_RESOURCE_NAME}-grpc-key-password"


@fixture(scope="function")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix.yaml"),
        name=MDBC_RESOURCE_NAME,
        namespace=namespace,
    )

    resource.set_version("8.3.4")

    # Add TLS configuration
    resource["spec"]["security"]["tls"] = {
        "enabled": True,
        "certificateKeySecretRef": {"name": TLS_SECRET_NAME},
        "caCertificateSecretRef": {"name": TLS_SECRET_NAME},
    }

    try_load(resource)
    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
    )

    # Add TLS configuration to MongoDBSearch
    if "spec" not in resource:
        resource["spec"] = {}

    resource["spec"]["security"] = {
        "tls": {
            "certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME},
            "keyFilePasswordSecretRef": {"name": GRPC_KEY_PASSWORD_SECRET_NAME},
        }
    }

    try_load(resource)
    return resource


@mark.e2e_search_community_tls
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_community_tls
def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    # Create user password secrets
    create_or_update_secret(namespace=namespace, name=f"{USER_NAME}-password", data={"password": USER_PASSWORD})
    create_or_update_secret(
        namespace=namespace, name=f"{ADMIN_USER_NAME}-password", data={"password": ADMIN_USER_PASSWORD}
    )
    create_or_update_secret(
        namespace=namespace, name=f"{mdbs.name}-{MONGOT_USER_NAME}-password", data={"password": MONGOT_USER_PASSWORD}
    )


@mark.e2e_search_community_tls
def test_install_tls_secrets_and_configmaps(namespace: str, mdbc: MongoDBCommunity, mdbs: MongoDBSearch, issuer: str):
    create_tls_certs(issuer, namespace, mdbc.name, mdbc["spec"]["members"], secret_name=TLS_SECRET_NAME)

    search_service_name = search_resource_names.mongot_service_name(mdbs.name)
    proxy_service_name = search_resource_names.proxy_service_name(mdbs.name)
    create_tls_certs(
        issuer,
        namespace,
        search_resource_names.mongot_statefulset_name(mdbs.name),
        replicas=1,
        service_name=search_service_name,
        additional_domains=[
            f"{search_service_name}.{namespace}.svc.cluster.local",
            f"{proxy_service_name}.{namespace}.svc.cluster.local",
        ],
        secret_name=MDBS_TLS_SECRET_NAME,
    )
    encrypt_tls_key_with_password(namespace, MDBS_TLS_SECRET_NAME, GRPC_KEY_PASSWORD)
    create_keyfile_password_secret(namespace, GRPC_KEY_PASSWORD_SECRET_NAME, GRPC_KEY_PASSWORD)


@mark.e2e_search_community_tls
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_community_tls
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_community_tls
def test_wait_for_community_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity, issuer_ca_filepath: str, namespace: str) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdbc, USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=issuer_ca_filepath),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_community_tls
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_community_tls
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_community_tls
def test_search_assert_search_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)


@mark.e2e_search_community_tls
def test_disable_search_ingress_tls_cleans_generated_resources(
    namespace: str,
    mdbc: MongoDBCommunity,
    mdbs: MongoDBSearch,
    sample_movies_helper: SampleMoviesSearchHelper,
):
    apps = client.AppsV1Api()
    core = client.CoreV1Api()
    sts_name = search_resource_names.mongot_statefulset_name(mdbs.name)
    generated_tls_secret_name = search_resource_names.operator_managed_tls_secret_name(mdbs.name)
    source_password_secret_name = f"{mdbs.name}-{MONGOT_USER_NAME}-password"
    source_config_secret_name = f"{mdbc.name}-config"
    source_pod_names = [f"{mdbc.name}-{i}" for i in range(mdbc["spec"]["members"])]

    def read_source_automation_config() -> dict:
        secret = core.read_namespaced_secret(source_config_secret_name, namespace)
        assert secret.data
        return json.loads(base64.b64decode(secret.data["cluster-config.json"]))

    customer_secret_uids = {
        name: core.read_namespaced_secret(name, namespace).metadata.uid
        for name in (
            TLS_SECRET_NAME,
            MDBS_TLS_SECRET_NAME,
            GRPC_KEY_PASSWORD_SECRET_NAME,
            source_password_secret_name,
        )
    }

    generated_tls_secret = core.read_namespaced_secret(generated_tls_secret_name, namespace)
    assert generated_tls_secret.metadata.uid
    sts = apps.read_namespaced_stateful_set(sts_name, namespace)
    mongot = next(container for container in sts.spec.template.spec.containers if container.name == "mongot")
    assert {"tls", "grpc-key-password", "password"} <= {volume.name for volume in sts.spec.template.spec.volumes}
    assert {"tls", "grpc-key-password", "password"} <= {mount.name for mount in mongot.volume_mounts}
    original_pod_uids = {
        pod_name: core.read_namespaced_pod(pod_name, namespace).metadata.uid for pod_name in (f"{sts_name}-0",)
    }

    mdbs.load()
    mdbs["spec"]["security"]["tls"] = None
    old_source_config_version = read_source_automation_config()["version"]
    mdbs.update()

    wait_for_all_pods_replaced(namespace, original_pod_uids, timeout=600)
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    wait_for_resource_deleted(
        lambda: core.read_namespaced_secret(generated_tls_secret_name, namespace),
        f"generated ingress TLS Secret {namespace}/{generated_tls_secret_name}",
        timeout=300,
    )

    sts = apps.read_namespaced_stateful_set(sts_name, namespace)
    mongot = next(container for container in sts.spec.template.spec.containers if container.name == "mongot")
    volume_names = {volume.name for volume in sts.spec.template.spec.volumes}
    mount_names = {mount.name for mount in mongot.volume_mounts}
    assert "tls" not in volume_names
    assert "grpc-key-password" not in volume_names
    assert "tls" not in mount_names
    assert "grpc-key-password" not in mount_names
    assert "password" in volume_names
    assert "password" in mount_names

    assert {
        name: core.read_namespaced_secret(name, namespace).metadata.uid for name in customer_secret_uids
    } == customer_secret_uids

    def source_reached_new_goal_state() -> tuple[bool, str]:
        automation_config = read_source_automation_config()
        version = automation_config["version"]
        tls_modes = {process["args2_6"]["setParameter"]["searchTLSMode"] for process in automation_config["processes"]}
        pods = [core.read_namespaced_pod(name, namespace) for name in source_pod_names]
        pod_states = []
        for pod in pods:
            agent_version = (pod.metadata.annotations or {}).get("agent.mongodb.com/version")
            ready = pod_is_ready(pod)
            pod_states.append(f"{pod.metadata.name}=agent_version:{agent_version},ready:{ready}")
        reached = (
            version > old_source_config_version
            and tls_modes == {"disabled"}
            and all(
                (pod.metadata.annotations or {}).get("agent.mongodb.com/version") == str(version) and pod_is_ready(pod)
                for pod in pods
            )
        )
        return reached, f"config_version={version}>{old_source_config_version}, tls_modes={tls_modes}, {pod_states}"

    run_periodically(
        source_reached_new_goal_state,
        timeout=600,
        sleep_time=3,
        msg="source MongoDBCommunity agents to reach the new automation-config goal state",
    )
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)
    sample_movies_helper.assert_search_query(retry_timeout=120)
