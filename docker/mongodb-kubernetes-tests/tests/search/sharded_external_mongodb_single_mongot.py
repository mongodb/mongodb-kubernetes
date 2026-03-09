"""
This test sharded cluster support for search, running with a single mongot instance per shard (replica set).

Deployment configuration:
  - MongoDB CR, sharded cluster, pretending to be deployed externally
  - MongoDBSearch: referencing external mongodb, one instance of mongot deployed per shard
"""

import pymongo
import pymongo.errors
from kubetester.kubetester import run_periodically
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
from tests.common.search.sharded_search_helper import *
from tests.conftest import get_default_operator, get_issuer_ca_filepath
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

# User credentials
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

# Ports
MONGOT_PORT = 27028

# Resource names
MDB_RESOURCE_NAME = "mdb-sh-single"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_sharded_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, sharded_ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    return helper.create_sharded_mdb(
        mongot_host_fn=lambda shard: search_resource_names.shard_service_host(
            MDBS_RESOURCE_NAME, shard, namespace, MONGOT_PORT
        ),
        set_tls_ca=True,
    )


@fixture(scope="function")
def mdbs(namespace: str, mdb: MongoDB, helper: SearchDeploymentHelper) -> MongoDBSearch:
    return helper.mdbs_for_ext_sharded_source(mongot_user_name=MONGOT_USER_NAME)


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}")


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(f"{MDB_RESOURCE_NAME}-{USER_NAME}")


@fixture(scope="function")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs, MONGOT_USER_NAME)


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_sharded_external_mongod_single_mongot
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    helper.install_sharded_tls_certificates()


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_create_sharded_cluster(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
    mdb: MongoDB,
):
    helper.deploy_users(
        admin_user,
        ADMIN_USER_PASSWORD,
        user,
        USER_PASSWORD,
        mongot_user,
        MONGOT_USER_PASSWORD,
    )


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_create_search_tls_certificate(namespace: str, issuer: str):
    """Create per-shard TLS certificates for MongoDBSearch resource."""
    create_per_shard_search_tls_certs(
        namespace=namespace,
        issuer=issuer,
        prefix=MDBS_TLS_CERT_PREFIX,
        mdb_resource_name=MDB_RESOURCE_NAME,
        shard_count=SHARD_COUNT,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
    )
    logger.info(f"Per-shard Search TLS certificates created with prefix: {MDBS_TLS_CERT_PREFIX}")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_create_search_resource(mdbs: MongoDBSearch):
    """Test MongoDBSearch resource deployment with external sharded source config."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_wait_for_sharded_cluster_ready(mdb: MongoDB):
    """Wait for sharded cluster to be ready after Search deployment."""
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_wait_for_agents_ready(mdb: MongoDB):
    """Wait for automation agents to be ready."""
    mdb.get_om_tester().wait_agents_ready()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


# TODO: We don't really need this, it can be removed if we have a way to figure out a logical time
# to wait for to get the mongod/mongos config properly generated.
@mark.e2e_search_sharded_external_mongod_single_mongot
def test_wait_for_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    verify_sharded_mongod_parameters(
        namespace, MDB_RESOURCE_NAME, mdbs.name, SHARD_COUNT,
        expected_host_fn=lambda shard: search_resource_names.shard_service_host(
            mdbs.name, shard, namespace, MONGOT_PORT
        ),
    )


# TODO: We don't really need this, it can be removed if we have a way to figure out a logical time
# to wait for to get the mongod/mongos config properly generated.
@mark.e2e_search_sharded_external_mongod_single_mongot
def test_verify_mongos_search_config(namespace: str, mdb: MongoDB):
    verify_mongos_search_config(namespace, MDB_RESOURCE_NAME)


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    # The tools_pod fixture handles deployment and waiting for readiness
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@fixture(scope="function")
def sample_movies_helper(mdb: MongoDB, namespace: str) -> movies_search_helper.SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_sharded(
            mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True, ca_path=get_issuer_ca_filepath()
        ),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_restore_sample_database(
    mdb: MongoDB, sample_movies_helper: movies_search_helper.SampleMoviesSearchHelper
):
    sample_movies_helper.restore_sample_database()
    logger.info("Sample database restored")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_shard_collections(mdb: MongoDB):
    search_tester = get_search_tester(mdb, f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}", ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.shard_and_distribute_collection("sample_mflix", "movies")
    logger.info("Collections sharded and chunks are distributed")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_verify_documents_count_in_shards(mdb: MongoDB):
    search_tester = get_search_tester(mdb, f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}", ADMIN_USER_PASSWORD, use_ssl=True)
    movies_helper = SampleMoviesSearchHelper(search_tester)

    total_docs = search_tester.client["sample_mflix"]["movies"].count_documents({})
    logger.info(f"Total documents in movies collection: {total_docs}")

    shard_counts = movies_helper.get_shard_document_counts()

    assert len(shard_counts) == SHARD_COUNT, f"Expected {SHARD_COUNT} shards, found {len(shard_counts)}"
    for shard_name, count in shard_counts.items():
        assert count > 0, f"Shard {shard_name} has 0 documents, data was not distributed"

    shard_total = sum(shard_counts.values())
    assert shard_total == total_docs, f"Sum of shard counts ({shard_total}) != total documents ({total_docs})"
    logger.info(f"Document distribution verified: {shard_counts}, total: {shard_total}")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_create_search_index(mdb: MongoDB):
    search_tester = get_search_tester(mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)
    logger.info("Text search index created")

    emb_helper = EmbeddedMoviesSearchHelper(search_tester)
    emb_helper.create_vector_search_index()
    emb_helper.wait_for_vector_search_index()
    logger.info("Vector search index is ready on embedded_movies")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_execute_text_search_query(mdb: MongoDB):
    search_tester = get_search_tester(mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_verify_results_from_all_shards(mdb: MongoDB):
    search_tester = get_search_tester(mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True)
    verify_search_results_from_all_shards(search_tester)


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_vector_search_before_and_after_sharding(mdb: MongoDB):
    search_tester = get_search_tester(mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True)
    admin_search_tester = get_search_tester(
        mdb, f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}", ADMIN_USER_PASSWORD, use_ssl=True
    )
    verify_vector_search_before_and_after_sharding(search_tester, admin_search_tester)
