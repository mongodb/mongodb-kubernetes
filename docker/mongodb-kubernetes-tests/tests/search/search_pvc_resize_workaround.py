import pymongo.errors
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_deployment_helper import (
    RESIZE_INITIAL_STORAGE,
    RESIZE_STORAGE_CLASS,
    SearchPVCResize,
    run_search_pvc_resize,
)
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

pytestmark = mark.e2e_search_pvc_resize_workaround

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"


@fixture(scope="module")
def resize_operator(namespace: str, operator_installation_config: dict[str, str]) -> Operator:
    return get_default_operator(namespace, operator_installation_config=operator_installation_config)


@fixture(scope="module")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix.yaml"), name="mdbc-rs", namespace=namespace
    )
    try_load(resource)
    return resource


@fixture(scope="module")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace)
    if not try_load(resource):
        resource["spec"]["clusters"][0]["persistence"] = {
            "single": {"storage": RESIZE_INITIAL_STORAGE, "storageClass": RESIZE_STORAGE_CLASS}
        }
    return resource


@fixture(scope="module")
def sample_movies_helper(mdbc: MongoDBCommunity, namespace: str) -> SampleMoviesSearchHelper:
    return SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdbc, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


def test_install_operator(resize_operator: Operator):
    resize_operator.wait_for_operator_ready()


def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    for name, password in (
        (USER_NAME, USER_PASSWORD),
        (ADMIN_USER_NAME, ADMIN_USER_PASSWORD),
        (f"{mdbs.name}-{MONGOT_USER_NAME}", MONGOT_USER_PASSWORD),
    ):
        create_or_update_secret(namespace, name=f"{name}-password", data={"password": password})


def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


def test_wait_for_community_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    def created() -> tuple[bool, str]:
        try:
            sample_movies_helper.create_search_index()
        except pymongo.errors.OperationFailure as exc:
            # SearchNotEnabled until mongod's rolling restart applies mongotHost.
            if exc.code != 31082:
                raise
            return False, str(exc)
        return True, "created"

    run_periodically(created, timeout=600, sleep_time=10, msg="search index creation")


def test_manual_resize_stops_and_resumes_operator(
    mdbs: MongoDBSearch, resize_operator: Operator, sample_movies_helper: SampleMoviesSearchHelper
):
    assert resize_operator.name is not None
    index = mdbs["spec"]["clusters"][0].get("index", 0)
    run_search_pvc_resize(
        [SearchPVCResize(mdbs, index, resize_operator.api_client, sample_movies_helper)],
        [(resize_operator.api_client, resize_operator.name)],
        sample_movies_helper,
    )
