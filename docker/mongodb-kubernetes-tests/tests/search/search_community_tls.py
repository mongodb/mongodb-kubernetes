from kubetester import create_or_update_secret, try_load
from kubetester.certs import create_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod
from tests.common.search import movies_search_helper
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBC_RESOURCE_NAME = "mdbc-rs"

TLS_SECRET_NAME = "tls-secret"

# MongoDBSearch TLS configuration
MDBS_TLS_SECRET_NAME = "mdbs-tls-secret"


@fixture(scope="function")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix.yaml"),
        name=MDBC_RESOURCE_NAME,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource.set_version("8.2.0")

    # Add TLS configuration
    resource["spec"]["security"]["tls"] = {
        "enabled": True,
        "certificateKeySecretRef": {"name": TLS_SECRET_NAME},
        "caCertificateSecretRef": {"name": TLS_SECRET_NAME},
    }

    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    # Add TLS configuration to MongoDBSearch
    if "spec" not in resource:
        resource["spec"] = {}

    resource["spec"]["security"] = {"tls": {"enabled": True, "certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}

    return resource


@mark.e2e_search_community_tls
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_community_tls
def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    # Create user password secrets
    create_or_update_secret(namespace=namespace, name=f"{USER_NAME}-password", data={"password": USER_PASSWORD})
    create_or_update_secret(
        namespace=namespace, name=f"{ADMIN_USER_NAME}-password", data={"password": ADMIN_USER_PASSWORD}
    )
    create_or_update_secret(
        namespace=namespace, name=f"{mdbs.name}-{MONGOT_USER_NAME}-password", data={"password": MONGOT_USER_PASSWORD}
    )


@mark.e2e_search_community_tls
def test_install_tls_secrets_and_configmaps(namespace: str, mdbc: MongoDBCommunity, mdbs: MongoDBSearch, issuer: str):
    create_tls_certs(issuer, namespace, mdbc.name, mdbc["spec"]["members"], secret_name=TLS_SECRET_NAME)

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


@mark.e2e_search_community_tls
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_community_tls
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_community_tls
def test_wait_for_community_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity, issuer_ca_filepath: str) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(get_connection_string(mdbc, USER_NAME, USER_PASSWORD), use_ssl=True, ca_path=issuer_ca_filepath),
        tools_pod=get_tools_pod(mdbc.namespace),
    )


@mark.e2e_search_community_tls
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_community_tls
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_community_tls
def test_search_assert_search_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)


def get_connection_string(mdbc: MongoDBCommunity, user_name: str, user_password: str) -> str:
    return f"mongodb://{user_name}:{user_password}@{mdbc.name}-0.{mdbc.name}-svc.{mdbc.namespace}.svc.cluster.local:27017/?replicaSet={mdbc.name}"
