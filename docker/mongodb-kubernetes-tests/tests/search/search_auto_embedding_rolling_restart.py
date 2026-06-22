import os
import time
import uuid
from datetime import datetime, timezone

import pymongo.errors

from kubernetes import client as k8s_client
from kubetester import create_or_update_secret, try_load
from kubetester.certs import create_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.connectivity import wait_for_all_pods_replaced
from tests.common.search.envoy_helpers import EnvoyProxy
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MONGOT_PORT = 27028
ENVOY_PROXY_PORT = 27029
ENVOY_ADMIN_PORT = 9901

MDBC_RESOURCE_NAME = "mdbc-rs"
MDBS_RESOURCE_NAME = MDBC_RESOURCE_NAME
MONGOT_USER_PASSWORD_SECRET_NAME = f"mdbc-rs-{MONGOT_USER_NAME}-password"

CA_CONFIGMAP_NAME = f"{MDBC_RESOURCE_NAME}-ca"
MDBC_TLS_SECRET_NAME = "mdbc-tls-secret"
MDBS_TLS_SECRET_NAME = search_resource_names.mongot_tls_cert_name(MDBC_RESOURCE_NAME)
ENVOY_PROXY_SVC_NAME = "envoy-proxy-svc"

EMBEDDING_INDEXING_KEY_ENV_VAR = "AI_MONGODB_EMBEDDING_INDEXING_KEY"
EMBEDDING_QUERY_KEY_ENV_VAR = "AI_MONGODB_EMBEDDING_QUERY_KEY"
VOYAGE_API_KEY_SECRET_NAME = "voyage-api-keys"
PROVIDER_ENDPOINT = "https://ai.mongodb.com/v1/embeddings"

DB_NAME = "sample_mflix"
COL_NAME = "movies"
SENTINEL_COUNT = 20


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
    resource["spec"]["security"]["tls"] = {
        "enabled": True,
        "certificateKeySecretRef": {"name": MDBC_TLS_SECRET_NAME},
        "caCertificateSecretRef": {"name": MDBC_TLS_SECRET_NAME},
    }
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
    resource["spec"]["clusters"] = [{"replicas": 2}]
    resource["spec"]["loadBalancer"] = {
        "unmanaged": {
            "endpoint": f"{ENVOY_PROXY_SVC_NAME}.{namespace}.svc.cluster.local:{ENVOY_PROXY_PORT}",
        }
    }
    resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}
    resource["spec"]["autoEmbedding"] = {
        "embeddingModelAPIKeySecret": {"name": VOYAGE_API_KEY_SECRET_NAME},
        "providerEndpoint": PROVIDER_ENDPOINT,
    }
    return resource


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity, issuer_ca_filepath: str, namespace: str) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdbc, USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=issuer_ca_filepath),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_auto_embedding_rolling_restart
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_auto_embedding_rolling_restart
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


@mark.e2e_search_auto_embedding_rolling_restart
def test_install_tls_certificates(
    namespace: str, mdbc: MongoDBCommunity, mdbs: MongoDBSearch, issuer: str, ca_configmap: str
):
    create_tls_certs(issuer, namespace, mdbc.name, mdbc["spec"]["members"], secret_name=MDBC_TLS_SECRET_NAME)

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


@mark.e2e_search_auto_embedding_rolling_restart
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_auto_embedding_rolling_restart
def test_deploy_envoy_certificates(envoy: EnvoyProxy, issuer: str):
    envoy.create_certificates(issuer)


@mark.e2e_search_auto_embedding_rolling_restart
def test_deploy_envoy_proxy(envoy: EnvoyProxy):
    envoy.deploy()


@mark.e2e_search_auto_embedding_rolling_restart
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_auto_embedding_rolling_restart
def test_wait_for_database_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_auto_embedding_rolling_restart
def test_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_auto_embedding_rolling_restart
def test_create_auto_embedding_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_auto_embedding_vector_search_index()


@mark.e2e_search_auto_embedding_rolling_restart
def test_wait_for_auto_embedding_index(sample_movies_helper: SampleMoviesSearchHelper):
    # initial sync embeds ~21k docs through the embedding API; 600s to ensure READY before the roll
    sample_movies_helper.search_tester.wait_for_search_indexes_ready(DB_NAME, COL_NAME, timeout=600)


@mark.e2e_search_auto_embedding_rolling_restart
def test_backlog_drains_through_roll(
    sample_movies_helper: SampleMoviesSearchHelper, namespace: str, mdbs: MongoDBSearch
):
    sts_name = search_resource_names.mongot_statefulset_name(mdbs.name)
    pod_names = [f"{sts_name}-0", f"{sts_name}-1"]

    core_v1 = k8s_client.CoreV1Api()
    old_uids = {name: core_v1.read_namespaced_pod(name=name, namespace=namespace).metadata.uid for name in pod_names}

    sentinels = _make_sentinel_docs(SENTINEL_COUNT)

    # trigger a real STS rolling restart then insert sentinels across the window
    _rollout_restart_sts(namespace, sts_name)
    sample_movies_helper.search_tester.client[DB_NAME][COL_NAME].insert_many(sentinels)

    wait_for_all_pods_replaced(namespace, old_uids, timeout=600)

    missing_titles = _poll_vector_search_for_sentinel_titles(sample_movies_helper, sentinels, timeout=300)
    if missing_titles:
        missing_docs = [s for s in sentinels if s["title"] in missing_titles]
        diagnostics = _diagnose_missing_sentinels(sample_movies_helper, missing_docs)
        assert False, f"sentinels never indexed after roll (backlog stalled):\n{diagnostics}"


def _make_sentinel_docs(n: int) -> list[dict]:
    tag = uuid.uuid4().hex[:8]
    return [
        {
            "_id": f"sentinel-roll-{tag}-{i}",
            "title": f"Sentinel Roll {tag} {i}",
            # distinctive plot text: query by this to find the sentinel as nearest neighbor
            "plot": f"A unique sentinel document inserted during rolling restart {tag} number {i}.",
        }
        for i in range(n)
    ]


def _rollout_restart_sts(namespace: str, sts_name: str) -> None:
    now = datetime.now(timezone.utc).isoformat()
    k8s_client.AppsV1Api().patch_namespaced_stateful_set(
        name=sts_name,
        namespace=namespace,
        body={"spec": {"template": {"metadata": {"annotations": {"kubectl.kubernetes.io/restartedAt": now}}}}},
    )


def _poll_vector_search_for_sentinel_titles(
    helper: SampleMoviesSearchHelper, sentinels: list[dict], timeout: int = 300
) -> set[str]:
    """Return titles NOT found via $vectorSearch within timeout. Empty set = all indexed."""
    not_found = {s["title"]: s["plot"] for s in sentinels}
    deadline = time.time() + timeout
    while not_found and time.time() < deadline:
        for title, plot in list(not_found.items()):
            try:
                # plot self-query → cosine ≈ 1.0 → target ranks #1; limit covers all sentinels (tight cluster)
                results = list(
                    helper.search_tester.client[DB_NAME][COL_NAME].aggregate(
                        [
                            {
                                "$vectorSearch": {
                                    "index": "vector_auto_embed_index",
                                    "path": "plot",
                                    "query": plot,
                                    "numCandidates": 150,
                                    "limit": SENTINEL_COUNT,
                                }
                            },
                            {"$project": {"title": 1, "_id": 0}},
                        ]
                    )
                )
                if any(r.get("title") == title for r in results):
                    del not_found[title]
            except pymongo.errors.OperationFailure:
                pass  # transient 503 from mongot during recovery; retry next cycle
        if not_found:
            time.sleep(5)
    return set(not_found.keys())


def _diagnose_missing_sentinels(helper: SampleMoviesSearchHelper, missing_docs: list[dict]) -> str:
    """For each missing sentinel emit: exists-in-mongo (insert confirmed) + indexed (embedding ran)."""
    lines = []
    col = helper.search_tester.client[DB_NAME][COL_NAME]
    for s in missing_docs:
        exists = col.find_one({"_id": s["_id"]}) is not None
        try:
            hits = list(
                col.aggregate(
                    [
                        {
                            "$vectorSearch": {
                                "index": "vector_auto_embed_index",
                                "path": "plot",
                                "query": s["plot"],
                                "numCandidates": 150,
                                "limit": 1,
                            }
                        },
                        {"$project": {"_id": 1}},
                    ]
                )
            )
            indexed = any(r.get("_id") == s["_id"] for r in hits)
        except Exception:
            indexed = False
        lines.append(f"  {s['title']}: exists={exists}, indexed={indexed}")
    return "\n".join(lines)
