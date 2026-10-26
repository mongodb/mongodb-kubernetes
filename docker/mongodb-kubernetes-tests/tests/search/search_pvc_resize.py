from uuid import uuid4

import pymongo.errors
from kubernetes import client
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.connectivity import MongotPVCResize, resizable_persistence
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBC_RESOURCE_NAME = "mdbc-rs"

INITIAL_STORAGE = "1Gi"
RESIZED_STORAGE = "2Gi"


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
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace)
    if not try_load(resource):
        resource["spec"]["clusters"][0]["persistence"] = resizable_persistence(INITIAL_STORAGE)
    return resource


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity, namespace: str) -> SampleMoviesSearchHelper:
    return SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdbc, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace, mdbc.get_version()),
    )


def set_storage(mdbs: MongoDBSearch, storage: str):
    mdbs.load()
    mdbs["spec"]["clusters"][0]["persistence"] = resizable_persistence(storage)
    mdbs.update()


def wait_for_pvc_resize_pending(mdbs: MongoDBSearch, sts_name: str):
    def pending() -> tuple[bool, str]:
        mdbs.load()
        status = mdbs["status"]
        resizing = {p["statefulsetName"] for p in status.get("pvc") or []}
        return status["phase"] == "Pending" and sts_name in resizing, f"status={status}"

    run_periodically(pending, timeout=300, sleep_time=2, msg=f"PVC resize of {sts_name} to be reported")


def example_search_titles(movies: SampleMoviesSearchHelper) -> list[str]:
    return sorted(row["title"] for row in movies.execute_example_search_query())


def assert_new_document_indexed(movies: SampleMoviesSearchHelper):
    title = f"probe{uuid4().hex}"
    movies.search_tester.client[movies.db_name][movies.col_name].insert_one({"title": title, "plot": "ingestion probe"})

    def indexed() -> tuple[bool, str]:
        try:
            rows = movies.text_search_movies(title)
        except pymongo.errors.PyMongoError as e:
            return False, f"error: {e}"
        return any(row["title"] == title for row in rows), f"{title} not found in {rows}"

    run_periodically(indexed, timeout=300, sleep_time=5, msg=f"{title} to be searchable")


@mark.e2e_search_pvc_resize
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_pvc_resize
def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    create_or_update_secret(namespace=namespace, name=f"{USER_NAME}-password", data={"password": USER_PASSWORD})
    create_or_update_secret(
        namespace=namespace, name=f"{ADMIN_USER_NAME}-password", data={"password": ADMIN_USER_PASSWORD}
    )
    create_or_update_secret(
        namespace=namespace, name=f"{mdbs.name}-{MONGOT_USER_NAME}-password", data={"password": MONGOT_USER_PASSWORD}
    )


@mark.e2e_search_pvc_resize
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_pvc_resize
def test_create_search_resource(mdbs: MongoDBSearch, mdbc: MongoDBCommunity):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_pvc_resize
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()
    sample_movies_helper.create_search_index()
    sample_movies_helper.assert_search_query(retry_timeout=300)


@mark.e2e_search_pvc_resize
def test_storage_increase_resizes_pvcs_in_place(
    namespace: str, mdbs: MongoDBSearch, sample_movies_helper: SampleMoviesSearchHelper
):
    sts_name = search_resource_names.mongot_statefulset_name(mdbs.name)
    resize = MongotPVCResize(namespace, sts_name)
    baseline = example_search_titles(sample_movies_helper)

    set_storage(mdbs, RESIZED_STORAGE)
    wait_for_pvc_resize_pending(mdbs, sts_name)
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)

    assert "pvc" not in mdbs["status"]
    resize.assert_resized(RESIZED_STORAGE)
    assert example_search_titles(sample_movies_helper) == baseline
    assert_new_document_indexed(sample_movies_helper)


@mark.e2e_search_pvc_resize
def test_storage_decrease_is_rejected(
    namespace: str, mdbs: MongoDBSearch, sample_movies_helper: SampleMoviesSearchHelper
):
    sts_name = search_resource_names.mongot_statefulset_name(mdbs.name)
    sts_uid = client.AppsV1Api().read_namespaced_stateful_set(sts_name, namespace).metadata.uid

    set_storage(mdbs, INITIAL_STORAGE)
    mdbs.assert_reaches_phase(
        Phase.Failed, msg_regexp="Can't update pvc and statefulset to a smaller storage", timeout=300
    )
    assert client.AppsV1Api().read_namespaced_stateful_set(sts_name, namespace).metadata.uid == sts_uid

    set_storage(mdbs, RESIZED_STORAGE)
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)
    sample_movies_helper.assert_search_query(retry_timeout=60)
