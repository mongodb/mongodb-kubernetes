import yaml
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

MDB_RESOURCE_NAME = "mdb-rs"


@fixture(scope="function")
def mdb(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)

    if try_load(resource):
        return resource

    return resource


@fixture(scope="function")
def admin_user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-mdb-admin.yaml"), namespace=namespace, name=ADMIN_USER_NAME
    )

    if try_load(resource):
        return resource

    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@fixture(scope="function")
def user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture("mongodbuser-mdb-user.yaml"), namespace=namespace, name=USER_NAME)

    if try_load(resource):
        return resource

    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@fixture(scope="function")
def mongot_user(namespace: str, mdbs: MongoDBSearch) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
        namespace=namespace,
        name=f"{mdbs.name}-{MONGOT_USER_NAME}",
    )

    if try_load(resource):
        return resource

    resource["spec"]["username"] = MONGOT_USER_NAME
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@mark.e2e_search_enterprise_basic
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_enterprise_basic
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_basic
def test_create_users(
    namespace: str, admin_user: MongoDBUser, user: MongoDBUser, mongot_user: MongoDBUser, mdb: MongoDB
):
    create_or_update_secret(
        namespace, name=admin_user["spec"]["passwordSecretKeyRef"]["name"], data={"password": ADMIN_USER_PASSWORD}
    )
    admin_user.create()
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace, name=user["spec"]["passwordSecretKeyRef"]["name"], data={"password": USER_PASSWORD}
    )
    user.create()
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace, name=mongot_user["spec"]["passwordSecretKeyRef"]["name"], data={"password": MONGOT_USER_PASSWORD}
    )
    mongot_user.create()
    # we deliberately don't wait for this user to be ready, because to be reconciled successfully it needs the searchCoordinator role
    # which the ReplicaSet reconciler will only define in the automation config after the MongoDBSearch resource is created.


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
def sample_movies_helper(mdb: MongoDB, issuer_ca_filepath: str) -> SampleMoviesSearchHelper:
    from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod

    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(get_connection_string(mdb, USER_NAME, USER_PASSWORD), use_ssl=False),
        tools_pod=get_tools_pod(mdb.namespace),
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


def get_connection_string(mdb: MongoDB, user_name: str, user_password: str) -> str:
    return f"mongodb://{user_name}:{user_password}@{mdb.name}-0.{mdb.name}-svc.{mdb.namespace}.svc.cluster.local:27017/?replicaSet={mdb.name}"
