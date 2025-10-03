import pymongo
import yaml
from kubernetes import client
from kubetester import create_or_update_secret, run_periodically, try_load, wait_until
from kubetester.certs import create_mongodb_tls_certs, create_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.search import movies_search_helper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator, get_issuer_ca_filepath
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

MDB_RESOURCE_NAME = "mdb-ent-tls"

# MongoDBSearch TLS configuration
MDBS_TLS_SECRET_NAME = "mdbs-tls-secret"

MDB_VERSION_WITHOUT_BUILT_IN_ROLE = "8.0.10-ent"
MDB_VERSION_WITH_BUILT_IN_ROLE = "8.2.0-ent"


@fixture(scope="function")
def mdb(namespace: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)
    resource.set_version(MDB_VERSION_WITHOUT_BUILT_IN_ROLE)

    if try_load(resource):
        return resource

    resource.configure_custom_tls(issuer_ca_configmap, "certs")

    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)

    if try_load(resource):
        return resource

    # Add TLS configuration to MongoDBSearch
    if "spec" not in resource:
        resource["spec"] = {}

    resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}

    return resource


@fixture(scope="function")
def admin_user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-mdb-admin.yaml"), namespace=namespace, name=ADMIN_USER_NAME
    )

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@fixture(scope="function")
def user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture("mongodbuser-mdb-user.yaml"), namespace=namespace, name=USER_NAME)

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
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

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource["spec"]["username"] = MONGOT_USER_NAME
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@mark.e2e_search_enterprise_tls
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_enterprise_tls
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_tls
def test_install_tls_secrets_and_configmaps(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, mdb.name, f"certs-{mdb.name}-cert", mdb.get_members())

    search_service_name = f"{mdbs.name}-search-svc"
    create_tls_certs(
        issuer,
        namespace,
        f"{mdbs.name}-search",
        replicas=1,
        service_name=search_service_name,
        additional_domains=[f"{search_service_name}.{namespace}.svc.cluster.local"],
        secret_name=MDBS_TLS_SECRET_NAME,
    )


@mark.e2e_search_enterprise_tls
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_tls
def test_create_users(
    namespace: str, admin_user: MongoDBUser, user: MongoDBUser, mongot_user: MongoDBUser, mdb: MongoDB
):
    create_or_update_secret(
        namespace, name=admin_user["spec"]["passwordSecretKeyRef"]["name"], data={"password": ADMIN_USER_PASSWORD}
    )
    admin_user.update()

    create_or_update_secret(
        namespace, name=user["spec"]["passwordSecretKeyRef"]["name"], data={"password": USER_PASSWORD}
    )
    user.update()

    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace, name=mongot_user["spec"]["passwordSecretKeyRef"]["name"], data={"password": MONGOT_USER_PASSWORD}
    )
    mongot_user.update()
    # we deliberately don't wait for this user to be ready, because to be reconciled successfully it needs the searchCoordinator role
    # which the ReplicaSet reconciler will only define in the automation config after the MongoDBSearch resource is created.


@mark.e2e_search_enterprise_tls
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_tls
def test_wait_for_mongod_parameters(mdb: MongoDB):
    # After search CR is deployed, MongoDB controller will pick it up
    # and start adding searchCoordinator role and search-related
    # parameters to the automation config.
    def check_mongod_parameters():
        parameters_are_set = True
        pod_parameters = []
        for idx in range(mdb.get_members()):
            mongod_config = yaml.safe_load(
                KubernetesTester.run_command_in_pod_container(
                    f"{mdb.name}-{idx}", mdb.namespace, ["cat", "/data/automation-mongod.conf"]
                )
            )
            set_parameter = mongod_config.get("setParameter", {})
            parameters_are_set = parameters_are_set and (
                "mongotHost" in set_parameter and "searchIndexManagementHostAndPort" in set_parameter
            )
            pod_parameters.append(f"pod {idx} setParameter: {set_parameter}")

        return parameters_are_set, f'Not all pods have mongot parameters set:\n{"\n".join(pod_parameters)}'

    run_periodically(check_mongod_parameters, timeout=200)


# After picking up MongoDBSearch CR, MongoDB reconciler will add mongod parameters.
# But it will not immediately mark the MongoDB CR as Pending
# spinning
@mark.e2e_search_enterprise_tls
def test_wait_for_database_resource_ready(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_tls
def test_validate_tls_connections(mdb: MongoDB, mdbs: MongoDBSearch, namespace: str):
    validate_tls_connections(mdb, mdbs, namespace)


@mark.e2e_search_enterprise_tls
def test_search_restore_sample_database(mdb: MongoDB):
    get_admin_sample_movies_helper(mdb).restore_sample_database()


@mark.e2e_search_enterprise_tls
def test_search_create_search_index(mdb: MongoDB):
    get_user_sample_movies_helper(mdb).create_search_index()


@mark.e2e_search_enterprise_tls
def test_search_assert_search_query(mdb: MongoDB):
    get_user_sample_movies_helper(mdb).assert_search_query(retry_timeout=60)


@mark.e2e_search_enterprise_tls
# This test class verifies if mongodb <8.2 can be upgraded to mongodb >=8.2
# For mongod <8.2 the operator is automatically creating searchCoordinator customRole.
# We test here that the role exists before upgrade, because
# after mongodb is upgraded, the role should be removed from AC
# From 8.2 searchCoordinator role is a built-in role.
class TestUpgradeMongod:
    def test_mongod_version(self, mdb: MongoDB):
        # This test is redundant when looking at the context of the full test file,
        # as we deploy MDB_VERSION_WITHOUT_BUILT_IN_ROLE initially
        # But it makes sense if we take into consideration TestUpgradeMongod test class alone.
        # This checks the most important prerequisite for this test class to work.
        # We check the version in case the test class is reused in another place
        # or executed again when running locally.
        mdb.tester(ca_path=get_issuer_ca_filepath(), use_ssl=True).assert_version(MDB_VERSION_WITHOUT_BUILT_IN_ROLE)

    def test_check_polyfilled_role_in_ac(self, mdb: MongoDB):
        custom_roles = mdb.get_automation_config_tester().automation_config.get("roles", [])
        assert len(custom_roles) > 0
        assert "searchCoordinator" in [role["role"] for role in custom_roles]

    def test_upgrade_to_mongo_8_2(self, mdb: MongoDB):
        mdb.set_version(MDB_VERSION_WITH_BUILT_IN_ROLE)
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_check_polyfilled_role_not_in_ac(self, mdb: MongoDB):
        custom_roles = mdb.get_automation_config_tester().automation_config.get("roles", [])
        assert len(custom_roles) >= 0
        assert "searchCoordinator" not in [role["role"] for role in custom_roles]

    def test_mongod_version_after_upgrade(self, mdb: MongoDB):
        mdb_tester = mdb.tester(ca_path=get_issuer_ca_filepath(), use_ssl=True)
        mdb_tester.assert_scram_sha_authentication(
            ADMIN_USER_NAME, ADMIN_USER_PASSWORD, "SCRAM-SHA-256", 1, ssl=True, tlsCAFile=get_issuer_ca_filepath()
        )
        mdb_tester.assert_version(MDB_VERSION_WITH_BUILT_IN_ROLE)

    def test_search_assert_search_query_after_upgrade(self, mdb: MongoDB):
        get_user_sample_movies_helper(mdb).assert_search_query(retry_timeout=60)


def get_connection_string(mdb: MongoDB, user_name: str, user_password: str) -> str:
    return f"mongodb://{user_name}:{user_password}@{mdb.name}-0.{mdb.name}-svc.{mdb.namespace}.svc.cluster.local:27017/?replicaSet={mdb.name}"


def get_admin_sample_movies_helper(mdb):
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            get_connection_string(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD),
            use_ssl=True,
            ca_path=get_issuer_ca_filepath(),
        )
    )


def get_user_sample_movies_helper(mdb):
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            get_connection_string(mdb, USER_NAME, USER_PASSWORD), use_ssl=True, ca_path=get_issuer_ca_filepath()
        )
    )


def validate_tls_connections(mdb: MongoDB, mdbs: MongoDBSearch, namespace: str):
    with pymongo.MongoClient(
        f"mongodb://{mdb.name}-0.{mdb.name}-svc.{namespace}.svc.cluster.local:27017/?replicaSet={mdb.name}",
        tls=True,
        tlsCAFile=get_issuer_ca_filepath(),
        tlsAllowInvalidHostnames=False,
        serverSelectionTimeoutMS=30000,
        connectTimeoutMS=20000,
    ) as mongodb_client:
        mongodb_info = mongodb_client.admin.command("hello")
        assert mongodb_info.get("ok") == 1, "MongoDB connection failed"

    with pymongo.MongoClient(
        f"mongodb://{mdbs.name}-search-svc.{namespace}.svc.cluster.local:27027",
        tls=True,
        tlsCAFile=get_issuer_ca_filepath(),
        tlsAllowInvalidHostnames=False,
        serverSelectionTimeoutMS=10000,
        connectTimeoutMS=10000,
    ) as search_client:
        search_info = search_client.admin.command("hello")
        assert search_info.get("ok") == 1, "MongoDBSearch connection failed"
