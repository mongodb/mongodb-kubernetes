import os

from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
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


@mark.e2e_search_auto_embedding_version_gate
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_auto_embedding_version_gate
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


@mark.e2e_search_auto_embedding_version_gate
def test_create_community_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_auto_embedding_version_gate
def test_version_gate_rejects_sub_min(mdbs: MongoDBSearch):
    mdbs["spec"]["version"] = "0.59.0"  # valid semver < minSearchImageVersionForEmbedding (0.60.0)
    mdbs["spec"]["autoEmbedding"] = {
        "embeddingModelAPIKeySecret": {"name": VOYAGE_API_KEY_SECRET_NAME},
        "providerEndpoint": PROVIDER_ENDPOINT,
    }
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Failed, timeout=120)
    msg = mdbs.get_status_message()
    assert "auto embeddings" in msg and "0.60.0" in msg, f"unexpected status message: {msg}"
