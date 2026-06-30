import os
import re

from kubernetes import client as k8s_client
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBC_RESOURCE_NAME = "mdbc-rs"
EMBEDDING_INDEXING_KEY_ENV_VAR = "AI_MONGODB_EMBEDDING_INDEXING_KEY"
EMBEDDING_QUERY_KEY_ENV_VAR = "AI_MONGODB_EMBEDDING_QUERY_KEY"
VOYAGE_API_KEY_SECRET_NAME = "voyage-api-keys"
PROVIDER_ENDPOINT = "https://ai.mongodb.com/v1/embeddings"
MONGOT_CONTAINER = "mongot"
EMBEDDING_SECRETS_PATH = "/etc/mongot/secrets"


@fixture(scope="function")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix.yaml"),
        name=MDBC_RESOURCE_NAME,
        namespace=namespace,
    )
    try_load(resource)
    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
    )
    try_load(resource)
    return resource


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity, namespace: str) -> SampleMoviesSearchHelper:
    from tests.common.search import movies_search_helper

    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdbc, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_auto_embedding_key_handling
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_auto_embedding_key_handling
def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    create_or_update_secret(namespace=namespace, name=f"{USER_NAME}-password", data={"password": USER_PASSWORD})
    create_or_update_secret(
        namespace=namespace, name=f"{ADMIN_USER_NAME}-password", data={"password": ADMIN_USER_PASSWORD}
    )
    create_or_update_secret(
        namespace=namespace,
        name=f"{mdbs.name}-{MONGOT_USER_NAME}-password",
        data={"password": MONGOT_USER_PASSWORD},
    )

    indexing_key = os.getenv(EMBEDDING_INDEXING_KEY_ENV_VAR)
    query_key = os.getenv(EMBEDDING_QUERY_KEY_ENV_VAR)
    if not indexing_key or not query_key:
        raise ValueError(
            f"Missing required environment variables: {EMBEDDING_INDEXING_KEY_ENV_VAR} and/or {EMBEDDING_QUERY_KEY_ENV_VAR}"
        )
    create_or_update_secret(
        namespace=namespace,
        name=VOYAGE_API_KEY_SECRET_NAME,
        data={"query-key": query_key, "indexing-key": indexing_key},
    )


@mark.e2e_search_auto_embedding_key_handling
def test_create_community_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_auto_embedding_key_handling
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs["spec"]["autoEmbedding"] = {
        "embeddingModelAPIKeySecret": {"name": VOYAGE_API_KEY_SECRET_NAME},
        "providerEndpoint": PROVIDER_ENDPOINT,
    }
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_auto_embedding_key_handling
def test_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_auto_embedding_key_handling
def test_keys_not_leaked(mdbs: MongoDBSearch, namespace: str):
    idx = os.environ[EMBEDDING_INDEXING_KEY_ENV_VAR]
    qry = os.environ[EMBEDDING_QUERY_KEY_ENV_VAR]
    assert idx and qry, "key values must be non-empty for the leak check to be meaningful"

    cm_name = search_resource_names.mongot_configmap_name(mdbs.name)
    cm = KubernetesTester.read_configmap(namespace, cm_name)
    for blob in cm.values():
        assert idx not in blob and qry not in blob, f"embedding key leaked into mongot ConfigMap {cm_name}"

    status_blob = str(mdbs.load()["status"])
    assert idx not in status_blob and qry not in status_blob, "embedding key leaked into CR status"

    sts_name = search_resource_names.mongot_statefulset_name(mdbs.name)
    core_v1 = k8s_client.CoreV1Api()
    replicas = k8s_client.AppsV1Api().read_namespaced_stateful_set(sts_name, namespace).spec.replicas or 1
    for ordinal in range(replicas):
        pod_name = f"{sts_name}-{ordinal}"
        logs = KubernetesTester.read_pod_logs(namespace, pod_name, container=MONGOT_CONTAINER)
        assert idx not in logs and qry not in logs, f"embedding key leaked into {pod_name} mongot pod logs"

        pod = core_v1.read_namespaced_pod(name=pod_name, namespace=namespace)
        ann = pod.metadata.annotations or {}
        h = ann.get("autoEmbeddingDetailsHash")
        # hash is SHA-256 encoded as base32 no-padding → 52 chars in [A-Z2-7]
        assert h and re.fullmatch(
            r"[A-Z2-7]{52}", h
        ), f"{pod_name} autoEmbeddingDetailsHash must be a base32 SHA-256 hash, got: {h!r}"
        assert h != idx and h != qry, f"{pod_name} autoEmbeddingDetailsHash must not equal a raw key"


@mark.e2e_search_auto_embedding_key_handling
def test_keys_file_mounted(mdbs: MongoDBSearch, namespace: str):
    sts_name = search_resource_names.mongot_statefulset_name(mdbs.name)
    pod_name = f"{sts_name}-0"
    out = KubernetesTester.run_command_in_pod_container(
        pod_name, namespace, ["ls", EMBEDDING_SECRETS_PATH], container=MONGOT_CONTAINER
    )
    assert "indexing-key" in out and "query-key" in out, f"key files not mounted under {EMBEDDING_SECRETS_PATH}: {out}"
