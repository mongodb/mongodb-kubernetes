from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.search import movies_search_helper
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBC_RESOURCE_NAME = "mdbc-rs"
MDBS_RESOURCE_NAME = "mdbs"


@fixture(scope="function")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix-external.yaml"),
        name=MDBC_RESOURCE_NAME,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    mongot_host = f"{MDBS_RESOURCE_NAME}-search-svc.{namespace}.svc.cluster.local:27028"
    if "additionalMongodConfig" not in resource["spec"]:
        resource["spec"]["additionalMongodConfig"] = {}
    if "setParameter" not in resource["spec"]["additionalMongodConfig"]:
        resource["spec"]["additionalMongodConfig"]["setParameter"] = {}

    resource["spec"]["additionalMongodConfig"]["setParameter"].update(
        {
            "mongotHost": mongot_host,
            "searchIndexManagementHostAndPort": mongot_host,
            "skipAuthenticationToSearchIndexManagementServer": False,
            "searchTLSMode": "disabled",
            "useGrpcForSearch": True,
        }
    )

    return resource


@fixture(scope="function")
def mdbs(namespace: str, mdbc: MongoDBCommunity) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        name=MDBS_RESOURCE_NAME,
        namespace=namespace,
    )

    seeds = [
        f"{mdbc.name}-{i}.{mdbc.name}-svc.{namespace}.svc.cluster.local:27017" for i in range(mdbc["spec"]["members"])
    ]

    resource["spec"] = {
        "source": {
            "external": {
                "hostAndPorts": seeds,
            },
            "passwordSecretRef": {"name": f"{MDBC_RESOURCE_NAME}-{MONGOT_USER_NAME}-password", "key": "password"},
            "username": MONGOT_USER_NAME,
        }
    }

    return resource


@mark.e2e_search_external_basic
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_external_basic
def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    create_or_update_secret(namespace=namespace, name=f"{USER_NAME}-password", data={"password": USER_PASSWORD})
    create_or_update_secret(
        namespace=namespace, name=f"{ADMIN_USER_NAME}-password", data={"password": ADMIN_USER_PASSWORD}
    )

    create_or_update_secret(
        namespace=namespace,
        name=f"{MDBC_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
        data={"password": MONGOT_USER_PASSWORD},
    )


@mark.e2e_search_external_basic
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_external_basic
def test_create_search_resource(mdbs: MongoDBSearch, mdbc: MongoDBCommunity):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_external_basic
def test_wait_for_community_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(get_connection_string(mdbc, USER_NAME, USER_PASSWORD))
    )


@mark.e2e_search_external_basic
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_external_basic
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_external_basic
def test_search_assert_search_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)


def get_connection_string(mdbc: MongoDBCommunity, user_name: str, user_password: str) -> str:
    return f"mongodb://{user_name}:{user_password}@{mdbc.name}-0.{mdbc.name}-svc.{mdbc.namespace}.svc.cluster.local:27017/?replicaSet={mdbc.name}"
