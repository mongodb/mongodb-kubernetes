from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.search import search_resource_names
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBC_RESOURCE_NAME = "mdbc-rs"
BAD_SECRET_NAME = "bad-embedding-secret"
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


@mark.e2e_search_auto_embedding_bad_secret
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_auto_embedding_bad_secret
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
    # deliberately missing the required "indexing-key" key
    create_or_update_secret(
        namespace=namespace,
        name=BAD_SECRET_NAME,
        data={"query-key": "placeholder"},
    )


@mark.e2e_search_auto_embedding_bad_secret
def test_create_community_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_auto_embedding_bad_secret
def test_missing_indexing_key_reconcile_error(mdbs: MongoDBSearch):
    mdbs["spec"]["autoEmbedding"] = {
        "embeddingModelAPIKeySecret": {"name": BAD_SECRET_NAME},
        "providerEndpoint": PROVIDER_ENDPOINT,
    }
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Failed, timeout=120)
    msg = mdbs.get_status_message()
    assert msg and 'Required key "indexing-key" is not present' in msg


@mark.e2e_search_auto_embedding_bad_secret
def test_no_crashlooping_mongot_pods(namespace: str, mdbs: MongoDBSearch):
    # ensureEmbeddingAPIKeySecret fails before the apply loop, so the mongot STS is never created
    sts_name = search_resource_names.mongot_statefulset_name(mdbs.name)
    pods = KubernetesTester.read_pod_labels(namespace, label_selector=f"app={sts_name}").items
    assert pods == [], f"mongot pods exist but STS should not have been created: {[p.metadata.name for p in pods]}"
