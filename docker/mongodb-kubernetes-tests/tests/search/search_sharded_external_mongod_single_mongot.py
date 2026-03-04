"""
This test sharded cluster support for search, running with a single mongot instance per shard (replica set).

Deployment configuration:
  - MongoDB CR, sharded cluster, pretending to be deployed externally
  - MongoDBSearch: referencing external mongodb, one instance of mongot deployed per shard
"""

import pymongo
import pymongo.errors
import yaml
from kubetester import create_or_update_secret, try_load
from kubetester.certs import create_sharded_cluster_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
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
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper, EmbeddedMoviesSearchHelper
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
def mdb(namespace: str, sharded_ca_configmap: str) -> MongoDB:
    """Fixture for sharded MongoDB cluster with TLS enabled and search configuration.

    For the "external MongoDB source" simulation scenario, the MongoDB sharded cluster
    needs to be created WITH search configuration from the beginning (pointing to Envoy
    proxy endpoints that will be deployed later). This is different from the internal
    MongoDB source scenario where the operator automatically applies search configuration.

    The search configuration includes:
    - shardOverrides: Each shard points to its own Envoy proxy service
    - mongos: Points to the first shard's Envoy proxy service for search routing
    """
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-sharded-cluster-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    # Configure OpsManager/CloudManager connection
    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)

    # Build shardOverrides configuration with search parameters for each shard
    # Each shard needs to point to its own Envoy proxy service
    shard_overrides = []
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        # mongot headless service name follows the pattern: <search-name>-search-0-<shard-name>-svc
        proxy_host = search_resource_names.shard_service_host(MDBS_RESOURCE_NAME, shard_name, namespace, MONGOT_PORT)

        shard_overrides.append(
            {
                "shardNames": [shard_name],
                "additionalMongodConfig": {
                    "setParameter": {
                        "mongotHost": proxy_host,
                        "searchIndexManagementHostAndPort": proxy_host,
                        "skipAuthenticationToSearchIndexManagementServer": False,
                        "skipAuthenticationToMongot": False,
                        "searchTLSMode": "requireTLS",
                        "useGrpcForSearch": True,
                    }
                },
            }
        )

    resource["spec"]["shardOverrides"] = shard_overrides
    resource["spec"]["security"]["tls"]["ca"] = CA_CONFIGMAP_NAME

    # Configure mongos with search parameters pointing to first shard's Envoy proxy
    first_shard_name = f"{MDB_RESOURCE_NAME}-0"
    mongos_proxy_host = search_resource_names.shard_service_host(
        MDBS_RESOURCE_NAME, first_shard_name, namespace, MONGOT_PORT
    )

    # Initialize mongos spec if not present
    if "mongos" not in resource["spec"]:
        resource["spec"]["mongos"] = {}

    resource["spec"]["mongos"]["additionalMongodConfig"] = {
        "setParameter": {
            "mongotHost": mongos_proxy_host,
            "searchIndexManagementHostAndPort": mongos_proxy_host,
            "skipAuthenticationToSearchIndexManagementServer": False,
            "skipAuthenticationToMongot": False,
            "searchTLSMode": "requireTLS",
            "useGrpcForSearch": True,
        }
    }

    return resource


@fixture(scope="function")
def mdbs(namespace: str, mdb: MongoDB) -> MongoDBSearch:
    """Fixture for MongoDBSearch with external sharded source configuration.

    This fixture dynamically builds the spec.source.external.sharded configuration
    based on the deployed MongoDB sharded cluster, treating it as an external source.
    """
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-sharded-external-mongod.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )

    if try_load(resource):
        return resource

    # Build the external sharded source configuration dynamically
    # Router hosts (mongos endpoints)
    router_hosts = [
        f"{MDB_RESOURCE_NAME}-mongos-{i}.{MDB_RESOURCE_NAME}-svc.{namespace}.svc.cluster.local:27017"
        for i in range(MONGOS_COUNT)
    ]

    # Shard configurations
    shards = []
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        shard_hosts = [
            f"{shard_name}-{member}.{MDB_RESOURCE_NAME}-sh.{namespace}.svc.cluster.local:27017"
            for member in range(MONGODS_PER_SHARD)
        ]
        shards.append(
            {
                "shardName": shard_name,
                "hosts": shard_hosts,
            }
        )

    # Set the external sharded source configuration
    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {
            "name": f"{resource.name}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
        "external": {
            "shardedCluster": {
                "router": {
                    "hosts": router_hosts,
                },
                "shards": shards,
            },
            "tls": {
                "ca": {
                    "name": CA_CONFIGMAP_NAME,
                },
            },
        },
    }

    return resource


@fixture(scope="function")
def admin_user(namespace: str) -> MongoDBUser:
    return make_admin_user(namespace, MDB_RESOURCE_NAME, f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}")


@fixture(scope="function")
def user(namespace: str) -> MongoDBUser:
    return make_user(namespace, MDB_RESOURCE_NAME, f"{MDB_RESOURCE_NAME}-{USER_NAME}")


@fixture(scope="function")
def mongot_user(namespace: str, mdbs: MongoDBSearch) -> MongoDBUser:
    return make_mongot_user(namespace, mdbs, MDB_RESOURCE_NAME, MONGOT_USER_NAME)


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
def test_install_tls_certificates(namespace: str, mdb: MongoDB, issuer: str):
    mongos_service_dns = f"{MDB_RESOURCE_NAME}-svc.{namespace}.svc.cluster.local"
    create_sharded_cluster_certs(
        namespace=namespace,
        resource_name=MDB_RESOURCE_NAME,
        shards=SHARD_COUNT,
        mongod_per_shard=MONGODS_PER_SHARD,
        config_servers=CONFIG_SERVER_COUNT,
        mongos=MONGOS_COUNT,
        secret_prefix="mdb-sh-",
        mongos_service_dns_names=[mongos_service_dns],
    )
    logger.info("Sharded cluster TLS certificates created")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_create_sharded_cluster(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_create_users(
    namespace: str,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
    mdb: MongoDB,
):
    """Test user creation for the sharded cluster."""
    create_or_update_secret(
        namespace,
        name=admin_user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": ADMIN_USER_PASSWORD},
    )
    admin_user.update()

    create_or_update_secret(
        namespace,
        name=user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": USER_PASSWORD},
    )
    user.update()

    create_or_update_secret(
        namespace,
        name=mongot_user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": MONGOT_USER_PASSWORD},
    )
    mongot_user.update()

    user.assert_reaches_phase(Phase.Updated, timeout=300)
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)
    # Don't wait for mongot user - it needs searchCoordinator role from Search CR


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
    def check_mongod_parameters():
        all_correct = True
        status_msgs = []

        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            pod_name = f"{shard_name}-0"

            try:
                mongod_config = yaml.safe_load(
                    KubernetesTester.run_command_in_pod_container(
                        pod_name, namespace, ["cat", "/data/automation-mongod.conf"]
                    )
                )
                expected_mongot_host_port = search_resource_names.shard_service_host(
                    mdbs.name, shard_name, namespace, MONGOT_PORT
                )

                set_parameter = mongod_config.get("setParameter", {})
                mongot_host = set_parameter.get("mongotHost", "")
                search_index_host = set_parameter.get("searchIndexManagementHostAndPort", "")

                if mongot_host != expected_mongot_host_port:
                    all_correct = False
                    status_msgs.append(f"Shard {shard_name}: mongotHost: {mongot_host} != {expected_mongot_host_port}")

                if search_index_host != expected_mongot_host_port:
                    all_correct = False
                    status_msgs.append(
                        f"Shard {shard_name}: searchIndexManagementHostAndPort: {search_index_host} != {expected_mongot_host_port}"
                    )
                else:
                    status_msgs.append(f"Shard {shard_name}: all hosts set correctly to {expected_mongot_host_port}")

            except Exception as e:
                all_correct = False
                status_msgs.append(f"Shard {shard_name}: Error - {e}")

        return all_correct, "\n".join(status_msgs)

    run_periodically(check_mongod_parameters, timeout=300, sleep_time=10, msg="mongod search parameters")
    logger.info("All shards have correct mongod search parameters pointing to Envoy proxy")


# TODO: We don't really need this, it can be removed if we have a way to figure out a logical time
# to wait for to get the mongod/mongos config properly generated.
@mark.e2e_search_sharded_external_mongod_single_mongot
def test_verify_mongos_search_config(namespace: str, mdb: MongoDB):
    mongos_pod = f"{MDB_RESOURCE_NAME}-mongos-0"

    def check_mongos_config():
        try:
            config = KubernetesTester.run_command_in_pod_container(
                mongos_pod, namespace, ["cat", f"/var/lib/mongodb-mms-automation/workspace/mongos-{mongos_pod}.conf"]
            )

            has_mongot_host = "mongotHost" in config
            has_search_mgmt = "searchIndexManagementHostAndPort" in config
            has_grpc = "useGrpcForSearch" in config

            status = f"mongotHost={has_mongot_host}, searchMgmt={has_search_mgmt}, grpc={has_grpc}"
            return has_mongot_host and has_search_mgmt, status
        except Exception as e:
            return False, f"Error: {e}"

    run_periodically(check_mongos_config, timeout=300, sleep_time=10, msg="mongos search config")
    logger.info("Mongos has correct search configuration")


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
    movies_helper = SampleMoviesSearchHelper(search_tester)

    def execute_search():
        try:
            results = movies_helper.text_search_movies("star wars")

            result_count = len(results)
            logger.info(f"Search returned {result_count} results")
            for r in results:
                logger.debug(f"  - {r.get('title')} (score: {r.get('score')})")

            if result_count > 0:
                return True, f"Search returned {result_count} results"
            return False, "Search returned no results"
        except pymongo.errors.PyMongoError as e:
            return False, f"Error: {e}"

    run_periodically(execute_search, timeout=60, sleep_time=5, msg="search query to succeed")
    logger.info("Text search query executed successfully through mongos")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_verify_results_from_all_shards(mdb: MongoDB):
    search_tester = get_search_tester(mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True)
    movies_helper = SampleMoviesSearchHelper(search_tester)
    # Get total document count
    total_docs = search_tester.client["sample_mflix"]["movies"].count_documents({})
    logger.info(f"Total documents in collection: {total_docs}")

    # we have a document in our movies collection whose title is `$`, that's it. And because
    # of that Lucene doesn't tokenize that document and as a result the respective entry is not
    # made/found in the Lucene Inverted index and that's where the wildcard query looks for data.
    # That's why we are expecting 1 less document because that one untokenzed data is not going
    # to be found ever in inverted index.
    expected_docs = total_docs - 1

    def execute_all_docs_search():
        # Execute wildcard search to get all documents
        results = movies_helper.wildcard_search_movies()
        search_count = len(results)
        logger.info(f"Search through mongos returned {search_count} documents")

        if search_count == expected_docs:
            return True, f""
        else:
            return (
                False,
                f"Search query for all documents returned {search_count} documents, expected were {expected_docs}",
            )

    run_periodically(execute_all_docs_search, timeout=120, sleep_time=5, msg="search query for all docs")
    logger.info(f"Search results for all documents verified.")

@mark.e2e_search_sharded_external_mongod_single_mongot
def test_vector_search_before_and_after_sharding(mdb: MongoDB):
    """Verify vector search returns consistent results before and after sharding embedded_movies."""
    search_tester = get_search_tester(mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True)
    admin_search_tester = get_search_tester(mdb, f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}", ADMIN_USER_PASSWORD, use_ssl=True)
    emb_helper = EmbeddedMoviesSearchHelper(search_tester)

    # Generate query vector by calling the Voyage embedding API
    query_vector = emb_helper.generate_query_vector("war movies")

    # Count total documents with embeddings to use as the limit — ensures we verify
    # ALL documents are reachable, not just an arbitrary subset.
    total_docs = emb_helper.count_documents_with_embeddings()
    logger.info(f"Total documents with embeddings: {total_docs}")

    # Run vector search before sharding
    results_before = emb_helper.vector_search(query_vector, limit=total_docs)
    count_before = len(results_before)
    logger.info(f"Vector search before sharding: {count_before} results")
    assert count_before > 0, "Vector search returned no results before sharding"

    # Shard the embedded_movies collection
    admin_search_tester.shard_and_distribute_collection("sample_mflix", "embedded_movies")
    logger.info("embedded_movies collection sharded")

    # Resharding (shard_and_distribute_collection) drops search indexes — recreate and wait for ready
    emb_helper.create_vector_search_index()
    emb_helper.wait_for_vector_search_index(timeout=300)
    logger.info("Vector search index recreated after resharding")

    # Run vector search after sharding with the same query vector and verify same count.
    # Catch OperationFailure because mongot shards may still be in INITIAL_SYNC after resharding.
    def verify_vector_search_after_sharding():
        try:
            results_after = emb_helper.vector_search(query_vector, limit=total_docs)
        except pymongo.errors.OperationFailure as e:
            logger.info(f"Vector search not ready yet: {e}")
            return False, f"Vector search failed: {e}"
        count_after = len(results_after)
        logger.info(f"Vector search after sharding: {count_after} results")
        if count_after == count_before:
            return True, f"Vector search returned {count_after} results (matches pre-sharding count)"
        return False, f"Vector search returned {count_after} results, expected {count_before}"

    run_periodically(verify_vector_search_after_sharding, timeout=300, sleep_time=10, msg="vector search after sharding")
    logger.info(f"Vector search returns consistent {count_before} results after sharding")

