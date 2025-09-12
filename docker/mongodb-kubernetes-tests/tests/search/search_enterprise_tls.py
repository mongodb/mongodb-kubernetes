import pymongo
import yaml
from kubetester import create_or_update_secret, try_load
from kubetester.certs import create_mongodb_tls_certs, create_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
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

# MongoDBSearch TLS configuration
MDBS_TLS_SECRET_NAME = "mdbs-tls-secret"


@fixture(scope="function")
def mdb(namespace: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

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

    resource["spec"]["security"] = {"tls": {"enabled": True, "certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}

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


@mark.e2e_search_enterprise_tls
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


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


@mark.e2e_search_enterprise_tls
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_tls
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


@mark.e2e_search_enterprise_tls
def test_validate_tls_connections(mdb: MongoDB, mdbs: MongoDBSearch, namespace: str, issuer_ca_filepath: str):
    with pymongo.MongoClient(
        f"mongodb://{mdb.name}-0.{mdb.name}-svc.{namespace}.svc.cluster.local:27017/?replicaSet={mdb.name}",
        tls=True,
        tlsCAFile=issuer_ca_filepath,
        tlsAllowInvalidHostnames=False,
        serverSelectionTimeoutMS=30000,
        connectTimeoutMS=20000,
    ) as mongodb_client:
        mongodb_info = mongodb_client.admin.command("hello")
        assert mongodb_info.get("ok") == 1, "MongoDB connection failed"

    with pymongo.MongoClient(
        f"mongodb://{mdbs.name}-search-svc.{namespace}.svc.cluster.local:27027",
        tls=True,
        tlsCAFile=issuer_ca_filepath,
        tlsAllowInvalidHostnames=False,
        serverSelectionTimeoutMS=10000,
        connectTimeoutMS=10000,
    ) as search_client:
        search_info = search_client.admin.command("hello")
        assert search_info.get("ok") == 1, "MongoDBSearch connection failed"


@mark.e2e_search_enterprise_tls
def test_search_restore_sample_database(mdb: MongoDB, issuer_ca_filepath: str):
    sample_movies_helper = movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            get_connection_string(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD), use_ssl=True, ca_path=issuer_ca_filepath
        )
    )
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_enterprise_tls
def test_search_create_search_index(mdb: MongoDB, issuer_ca_filepath: str):
    sample_movies_helper = movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(get_connection_string(mdb, USER_NAME, USER_PASSWORD), use_ssl=True, ca_path=issuer_ca_filepath)
    )
    sample_movies_helper.create_search_index()


@mark.e2e_search_enterprise_tls
def test_search_assert_search_query(mdb: MongoDB, issuer_ca_filepath: str):
    sample_movies_helper = movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(get_connection_string(mdb, USER_NAME, USER_PASSWORD), use_ssl=True, ca_path=issuer_ca_filepath)
    )
    sample_movies_helper.assert_search_query(retry_timeout=60)


def get_connection_string(mdb: MongoDB, user_name: str, user_password: str) -> str:
    return f"mongodb://{user_name}:{user_password}@{mdb.name}-0.{mdb.name}-svc.{mdb.namespace}.svc.cluster.local:27017/?replicaSet={mdb.name}"
