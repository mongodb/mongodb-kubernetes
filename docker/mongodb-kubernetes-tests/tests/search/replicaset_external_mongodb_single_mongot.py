import pymongo
import pymongo.errors
import yaml
from kubetester import run_periodically
from kubetester.certs import create_mongodb_tls_certs, create_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.movies_search_helper import EmbeddedMoviesSearchHelper, SampleMoviesSearchHelper
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_sharded_ca
from tests.conftest import get_default_operator, get_issuer_ca_filepath
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-rs-ext"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
MDBS_TLS_SECRET_NAME = search_resource_names.mongot_tls_cert_name(MDB_RESOURCE_NAME)


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_sharded_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, ca_configmap: str, issuer_ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    mongot_host = search_resource_names.mongot_service_host(MDBS_RESOURCE_NAME, namespace, 27028)
    return helper.create_replicaset_mdb(
        mongot_host=mongot_host,
        issuer_ca_configmap=issuer_ca_configmap,
        tls_cert_prefix="certs",
    )


@fixture(scope="function")
def mdbs(namespace: str, helper: SearchDeploymentHelper) -> MongoDBSearch:
    resource = helper.mdbs_for_ext_rs_source(mongot_user_name=MONGOT_USER_NAME)
    resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}
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


@mark.e2e_search_external_rs_single_mongot
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_external_rs_single_mongot
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_external_rs_single_mongot
def test_install_tls_certificates(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, mdb.name, f"certs-{mdb.name}-cert", mdb.get_members())

    search_service_name = search_resource_names.mongot_service_name(mdbs.name)
    create_tls_certs(
        issuer,
        namespace,
        search_resource_names.mongot_statefulset_name(mdbs.name),
        replicas=1,
        service_name=search_service_name,
        additional_domains=[f"{search_service_name}.{namespace}.svc.cluster.local"],
        secret_name=MDBS_TLS_SECRET_NAME,
    )


@mark.e2e_search_external_rs_single_mongot
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_external_rs_single_mongot
def test_create_users(
    helper: SearchDeploymentHelper, admin_user: MongoDBUser, user: MongoDBUser, mongot_user: MongoDBUser, mdb: MongoDB
):
    helper.deploy_users(
        admin_user,
        ADMIN_USER_PASSWORD,
        user,
        USER_PASSWORD,
        mongot_user,
        MONGOT_USER_PASSWORD,
    )


@mark.e2e_search_external_rs_single_mongot
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_external_rs_single_mongot
def test_wait_for_database_resource_ready(mdb: MongoDB):
    mdb.get_om_tester().wait_agents_ready()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_external_rs_single_mongot
def test_wait_for_mongod_parameters(mdb: MongoDB):
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

    run_periodically(check_mongod_parameters, timeout=600)


@fixture(scope="function")
def sample_movies_helper(mdb: MongoDB, namespace: str) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdb, USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=get_issuer_ca_filepath()),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_external_rs_single_mongot
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_external_rs_single_mongot
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_external_rs_single_mongot
def test_search_assert_search_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)


@mark.e2e_search_external_rs_single_mongot
def test_vector_search(mdb: MongoDB):
    search_tester = SearchTester.for_replicaset(
        mdb, USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=get_issuer_ca_filepath()
    )
    emb_helper = EmbeddedMoviesSearchHelper(search_tester)
    emb_helper.create_vector_search_index()
    emb_helper.wait_for_vector_search_index()

    query_vector = emb_helper.generate_query_vector("war movies")
    total_docs = emb_helper.count_documents_with_embeddings()

    # wait_for_vector_search_index checks that the index reports ready, but mongot may
    # still be in INITIAL_SYNC. The retry loop below handles that by catching OperationFailure.
    def verify_vector_search():
        try:
            results = emb_helper.vector_search(query_vector, limit=total_docs)
            count = len(results)
            if count > 0:
                return True, f"Vector search returned {count} results"
            return False, "Vector search returned no results"
        except pymongo.errors.OperationFailure as e:
            return False, f"Vector search failed: {e}"

    run_periodically(verify_vector_search, timeout=120, sleep_time=5, msg="vector search")
