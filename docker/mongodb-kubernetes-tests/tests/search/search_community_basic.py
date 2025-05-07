from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from pytest import fixture, mark
from tests import test_logger
from tests.common.search import movies_search_helper
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

USER_PASSWORD = "Passw0rd."
USER_NAME = "my-user"
MDBC_RESOURCE_NAME = "mdbc-rs"


@fixture(scope="function")
def mdbc(namespace: str, custom_mdb_version: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix.yaml"),
        name=MDBC_RESOURCE_NAME,
        namespace=namespace,
    )

    resource["spec"]["version"] = custom_mdb_version

    if try_load(resource):
        return resource

    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    return resource


@mark.e2e_search_community_basic
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_community_basic
def test_install_secret(namespace: str):
    create_or_update_secret(namespace=namespace, name="my-user-password", data={"password": USER_PASSWORD})


@mark.e2e_search_community_basic
def test_install_community_private_preview_pullsecret(namespace: str):
    import base64
    import os

    docker_config_json = os.environ.get("COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON")
    assert (
        docker_config_json != ""
    ), "COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON not found in environment variables"

    # Decode the base64 encoded docker config
    try:
        decoded_docker_config_json = base64.b64decode(docker_config_json).decode("utf-8")
    except Exception as e:
        logger.error(f"Failed to decode docker config: {e}")
        raise

    secret_name = "community-private-preview-pullsecret"

    create_or_update_secret(
        namespace=namespace,
        name=secret_name,
        data={".dockerconfigjson": decoded_docker_config_json},
        type="kubernetes.io/dockerconfigjson",
    )

    from kubernetes import client, config

    sa_name = "mongodb-kubernetes-database-pods"
    sa = client.CoreV1Api().read_namespaced_service_account(name=sa_name, namespace=namespace)

    if not sa.image_pull_secrets:
        sa.image_pull_secrets = []

    for secret in sa.image_pull_secrets:
        if secret == secret_name:
            break

    sa.image_pull_secrets.append(client.V1LocalObjectReference(name=secret_name))
    client.CoreV1Api().patch_namespaced_service_account(name=sa_name, namespace=namespace, body=sa)
    logger.info(f"Updated service account {sa_name} with community-private-preview-pullsecret")


@mark.e2e_search_community_basic
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=1000)


@mark.e2e_search_community_basic
def test_create_search_resource(mdbs: MongoDBSearch, mdbc: MongoDBCommunity):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_community_basic
def test_wait_for_community_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=1800)


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(SearchTester(get_connection_string(mdbc)))


@mark.e2e_search_community_basic
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_community_basic
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_community_basic
def test_search_wait_for_search_indexes(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.wait_for_search_indexes()


@mark.e2e_search_community_basic
def test_search_assert_search_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query()


def get_connection_string(mdbc: MongoDBCommunity) -> str:
    return f"mongodb://{USER_NAME}:{USER_PASSWORD}@{mdbc.name}-0.{mdbc.name}-svc.{mdbc.namespace}.svc.cluster.local:27017/?replicaSet={mdbc.name}"
