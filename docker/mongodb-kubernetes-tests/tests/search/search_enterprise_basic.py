import yaml
import yaml
from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

MDB_RESOURCE_NAME = "mdb-rs"


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDB_RESOURCE_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)
    try_load(resource)
    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)
    try_load(resource)
    return resource


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(ADMIN_USER_NAME)


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(USER_NAME)


@fixture(scope="function")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs, MONGOT_USER_NAME)


@mark.e2e_search_enterprise_basic
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_enterprise_basic
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_basic
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_basic
def test_create_users(
    helper: SearchDeploymentHelper, admin_user: MongoDBUser, user: MongoDBUser, mongot_user: MongoDBUser, mdb: MongoDB
):
    helper.deploy_users(
        admin_user, ADMIN_USER_PASSWORD,
        user, USER_PASSWORD,
        mongot_user, MONGOT_USER_PASSWORD,
    )


@mark.e2e_search_enterprise_basic
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_basic
def test_wait_for_database_resource_ready(mdb: MongoDB):
    mdb.assert_abandons_phase(Phase.Running, timeout=300)
    mdb.assert_reaches_phase(Phase.Running, timeout=300)

    for idx in range(mdb.get_members()):
        mongod_config = yaml.safe_load(
            KubernetesTester.run_command_in_pod_container(
                f"{mdb.name}-{idx}", mdb.namespace, ["cat", "/data/automation-mongod.conf"]
            )
        )
        setParameter = mongod_config.get("setParameter", {})
        assert (
            "mongotHost" in setParameter and "searchIndexManagementHostAndPort" in setParameter
        ), "mongot parameters not found in mongod config"


@fixture(scope="function")
def sample_movies_helper(mdb: MongoDB, namespace: str) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdb, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_enterprise_basic
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_enterprise_basic
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_enterprise_basic
def test_search_assert_search_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)
