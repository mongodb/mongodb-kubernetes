"""
E2E test for Community ReplicaSet MongoDB Search with auto-embedding and multiple mongot instances.

This test verifies the per-pod mongot config feature end-to-end:
- When autoEmbedding is set, the operator generates per-pod leader/follower
  configs in the mongot ConfigMap instead of a single config.yml.
- Pod {stsName}-0 gets role 'leader' (with IsAutoEmbeddingViewWriter: true), all others get 'follower'.
- The startup script uses $HOSTNAME to pick the correct config file per pod.

Test combines:
- search_community_auto_embedding.py: auto-embedding with Voyage API keys
- replicaset_internal_mongodb_multi_mongot_unmanaged_lb.py: multi-mongot with Envoy proxy
"""

import os

from kubetester import create_or_update_secret, read_configmap, try_load
from kubetester.certs import create_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.envoy_helpers import EnvoyProxy
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

# User credentials
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

# Ports
MONGOT_PORT = 27028
ENVOY_PROXY_PORT = 27029
ENVOY_ADMIN_PORT = 9901

# Resource names
MDBC_RESOURCE_NAME = "mdbc-rs-auto-embed-multi"
MDBS_RESOURCE_NAME = MDBC_RESOURCE_NAME

# Must match passwordSecretRef.name in community-replicaset-sample-mflix.yaml (fixture base name is "mdbc-rs")
MONGOT_USER_PASSWORD_SECRET_NAME = f"mdbc-rs-{MONGOT_USER_NAME}-password"

# TLS configuration
CA_CONFIGMAP_NAME = f"{MDBC_RESOURCE_NAME}-ca"
MDBS_TLS_SECRET_NAME = search_resource_names.mongot_tls_cert_name(MDBC_RESOURCE_NAME)

# Envoy proxy
ENVOY_PROXY_SVC_NAME = "envoy-proxy-svc"

# Auto-embedding configuration
EMBEDDING_INDEXING_KEY_ENV_VAR = "AI_MONGODB_EMBEDDING_INDEXING_KEY"
EMBEDDING_QUERY_KEY_ENV_VAR = "AI_MONGODB_EMBEDDING_QUERY_KEY"
VOYAGE_API_KEY_SECRET_NAME = "voyage-api-keys"
PROVIDER_ENDPOINT = "https://ai.mongodb.com/v1/embeddings"


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def envoy(namespace: str) -> EnvoyProxy:
    return EnvoyProxy(
        namespace=namespace,
        ca_configmap_name=CA_CONFIGMAP_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        mongot_port=MONGOT_PORT,
        envoy_proxy_port=ENVOY_PROXY_PORT,
        envoy_admin_port=ENVOY_ADMIN_PORT,
    )


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
def mdbs(namespace: str, mdbc: MongoDBCommunity) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
        name=MDBC_RESOURCE_NAME,
    )

    if try_load(resource):
        return resource

    resource["spec"]["source"] = {"passwordSecretRef": {"name": MONGOT_USER_PASSWORD_SECRET_NAME}}
    resource["spec"]["replicas"] = 2
    resource["spec"]["lb"] = {
        "mode": "Unmanaged",
        "endpoint": f"{ENVOY_PROXY_SVC_NAME}.{namespace}.svc.cluster.local:{ENVOY_PROXY_PORT}",
    }
    resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}
    resource["spec"]["autoEmbedding"] = {
        "embeddingModelAPIKeySecret": {"name": VOYAGE_API_KEY_SECRET_NAME},
        "providerEndpoint": PROVIDER_ENDPOINT,
    }
    return resource


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity, namespace: str) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdbc, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    create_or_update_secret(namespace=namespace, name=f"{USER_NAME}-password", data={"password": USER_PASSWORD})
    create_or_update_secret(
        namespace=namespace, name=f"{ADMIN_USER_NAME}-password", data={"password": ADMIN_USER_PASSWORD}
    )
    create_or_update_secret(
        namespace=namespace,
        name=MONGOT_USER_PASSWORD_SECRET_NAME,
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


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_install_tls_certificates(namespace: str, mdbs: MongoDBSearch, issuer: str, ca_configmap: str):
    """Create TLS certificates for mongot (replicas=2)."""
    search_service_name = search_resource_names.mongot_service_name(mdbs.name)
    create_tls_certs(
        issuer,
        namespace,
        search_resource_names.mongot_statefulset_name(mdbs.name),
        replicas=2,
        service_name=search_service_name,
        additional_domains=[f"{search_service_name}.{namespace}.svc.cluster.local"],
        secret_name=MDBS_TLS_SECRET_NAME,
    )


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_deploy_envoy_certificates(envoy: EnvoyProxy, issuer: str):
    envoy.create_certificates(issuer)


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_deploy_envoy_proxy(envoy: EnvoyProxy):
    """Deploy Envoy proxy for L7 load balancing."""
    envoy.deploy()


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_create_search_resource(mdbs: MongoDBSearch):
    """Deploy MongoDBSearch with replicas=2, unmanaged LB, and autoEmbedding."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_verify_per_pod_config(namespace: str):
    """Verify the ConfigMap contains per-pod leader/follower configs, not a single config.yml."""
    configmap_name = search_resource_names.mongot_configmap_name(MDBS_RESOURCE_NAME)
    sts_name = search_resource_names.mongot_statefulset_name(MDBS_RESOURCE_NAME)

    data = read_configmap(namespace, configmap_name)

    assert "config-leader.yml" in data, f"Expected 'config-leader.yml' key in ConfigMap {configmap_name}"
    assert "config-follower.yml" in data, f"Expected 'config-follower.yml' key in ConfigMap {configmap_name}"
    assert (
        "config.yml" not in data
    ), f"Expected 'config.yml' to be absent in ConfigMap {configmap_name} (per-pod path replaces it)"

    # Verify pod-name-to-role mappings
    leader_pod = f"{sts_name}-0"
    follower_pod = f"{sts_name}-1"
    assert (
        data.get(leader_pod) == "leader"
    ), f"Expected pod {leader_pod} to have role 'leader', got: {data.get(leader_pod)}"
    assert (
        data.get(follower_pod) == "follower"
    ), f"Expected pod {follower_pod} to have role 'follower', got: {data.get(follower_pod)}"

    logger.info(
        f"Per-pod config verified: {leader_pod}=leader, {follower_pod}=follower, "
        f"config-leader.yml and config-follower.yml present, config.yml absent"
    )


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_wait_for_database_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_search_create_auto_embedding_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_auto_embedding_vector_search_index()


@mark.e2e_search_community_auto_embedding_multi_mongot
def test_search_assert_auto_embedding_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_auto_emb_vector_search_query(retry_timeout=90)
