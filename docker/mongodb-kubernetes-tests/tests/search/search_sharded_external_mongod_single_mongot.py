"""
This test sharded cluster support for search, running with a single mongot instance per shard (replica set).

Deployment configuration:
  - MongoDB CR, sharded cluster, pretending to be deployed externally
  - MongoDBSearch: referencing external mongodb, one instance of mongot deployed per shard
"""

import time

import pymongo
import pymongo.errors
import yaml
from kubetester import create_or_update_secret, get_service, try_load
from kubetester.certs import create_sharded_cluster_certs, create_tls_certs
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
from tests.common.search.search_tester import SearchTester
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
MDB_RESOURCE_NAME = "mdb-sh"
MDBS_RESOURCE_NAME = "mdb-sh-search"
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# TLS configuration
# Per-shard TLS: each shard gets its own certificate with naming pattern:
# {prefix}-{shardName}-search-cert (e.g., certs-mdb-sh-0-search-cert)
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = "mdb-sh-ca"


# ============================================================================
# Fixtures
# ============================================================================


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """Create CA ConfigMap and Secret with the name expected by the sharded cluster (mdb-sh-ca).

    The MongoDB operator expects a ConfigMap with keys "ca-pem" and "mms-ca.crt".
    The Search controller expects a Secret with key "ca.crt" for mTLS.
    Both are created with the same name (mdb-sh-ca) but different resource types.
    """
    from kubetester import create_or_update_configmap, create_or_update_secret

    ca = open(issuer_ca_filepath).read()
    # The MongoDB operator expects the CA in entries named "ca-pem" and "mms-ca.crt"
    configmap_data = {"ca-pem": ca, "mms-ca.crt": ca}
    create_or_update_configmap(namespace, CA_CONFIGMAP_NAME, configmap_data)

    # The Search controller expects a Secret with key "ca.crt" for mTLS ingress
    secret_data = {"ca.crt": ca}
    create_or_update_secret(namespace, CA_CONFIGMAP_NAME, secret_data)

    return CA_CONFIGMAP_NAME


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
        # Envoy proxy service name follows the pattern: <search-name>-mongot-<shard-name>-svc
        proxy_host = f"{MDBS_RESOURCE_NAME}-mongot-{shard_name}-svc.{namespace}.svc.cluster.local:{MONGOT_PORT}"

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

    # Configure mongos with search parameters pointing to first shard's Envoy proxy
    first_shard_name = f"{MDB_RESOURCE_NAME}-0"
    mongos_proxy_host = (
        f"{MDBS_RESOURCE_NAME}-mongot-{first_shard_name}-svc.{namespace}.svc.cluster.local:{MONGOT_PORT}"
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
            "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
        "external": {
            "sharded": {
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
    """Fixture for admin user."""
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-mdb-admin.yaml"),
        namespace=namespace,
        name=ADMIN_USER_NAME,
    )

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@fixture(scope="function")
def user(namespace: str) -> MongoDBUser:
    """Fixture for regular user."""
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-mdb-user.yaml"),
        namespace=namespace,
        name=USER_NAME,
    )

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource


@fixture(scope="function")
def mongot_user(namespace: str, mdbs: MongoDBSearch) -> MongoDBUser:
    """Fixture for mongot sync user."""
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


@fixture(scope="module")
def tools_pod(namespace: str) -> mongodb_tools_pod.ToolsPod:
    """Fixture for MongoDB tools pod used for running mongorestore."""
    return mongodb_tools_pod.get_tools_pod(namespace)


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


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_verify_per_shard_services(namespace: str, mdbs: MongoDBSearch):
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        service_name = f"{mdbs.name}-mongot-{shard_name}-svc"

        logger.info(f"Checking for per-shard Service: {service_name}")

        service = get_service(namespace, service_name)
        assert service is not None, f"Per-shard Service {service_name} not found"

        ports = {p.port for p in service.spec.ports}
        assert MONGOT_PORT in ports, f"Service {service_name} missing mongot port {MONGOT_PORT}"

        logger.info(f"Per-shard Service {service_name} exists with ports: {ports}")


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
                expected_mongot_host_port = (
                    f"{mdbs.name}-mongot-{shard_name}-svc.{namespace}.svc.cluster.local:{MONGOT_PORT}"
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


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    search_tester = get_admin_search_tester(mdb, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )
    logger.info("Sample database restored")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_shard_collections(mdb: MongoDB):
    search_tester = get_admin_search_tester(mdb, use_ssl=True)
    client = search_tester.client
    admin_db = client.admin
    sample_mflix_db = client["sample_mflix"]

    # Enable sharding on database
    try:
        admin_db.command("enableSharding", "sample_mflix")
        logger.info("Sharding enabled on sample_mflix database")
    except pymongo.errors.OperationFailure as e:
        if (
            "already enabled" in str(e) or e.code == 23 or e.code == 59
        ):  # AlreadyInitialized or CommandNotFound (MongoDB 8.0+)
            logger.info("Sharding already enabled on sample_mflix")
        else:
            raise

    # Shard movies collection
    try:
        sample_mflix_db["movies"].create_index([("_id", pymongo.HASHED)])
        admin_db.command("shardCollection", "sample_mflix.movies", key={"_id": "hashed"})
        logger.info("movies collection sharded")
    except pymongo.errors.OperationFailure as e:
        if "already sharded" in str(e) or e.code == 20:  # AlreadyInitialized for sharding
            logger.info("movies collection already sharded")
        else:
            raise

    # Shard embedded_movies collection
    try:
        sample_mflix_db["embedded_movies"].create_index([("_id", pymongo.HASHED)])
        admin_db.command("shardCollection", "sample_mflix.embedded_movies", key={"_id": "hashed"})
        logger.info("embedded_movies collection sharded")
    except pymongo.errors.OperationFailure as e:
        if "already sharded" in str(e) or e.code == 20:  # AlreadyInitialized for sharding
            logger.info("embedded_movies collection already sharded")
        else:
            raise

    # Wait for balancer to distribute chunks
    # TODO: execute mdb command to wait for the balancer
    time.sleep(10)
    logger.info("Collections sharded and balanced")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_create_search_index(mdb: MongoDB):
    search_tester = get_user_search_tester(mdb, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    logger.info("Text search index created")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_wait_for_search_index_ready(mdb: MongoDB):
    search_tester = get_user_search_tester(mdb, use_ssl=True)
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)
    logger.info("Search index is ready")


@mark.e2e_search_sharded_external_mongod_single_mongot
def test_search_assert_search_query(mdb: MongoDB):
    search_tester = get_user_search_tester(mdb, use_ssl=True)

    def execute_search():
        try:
            results = list(
                search_tester.client["sample_mflix"]["movies"].aggregate(
                    [
                        {"$search": {"index": "default", "text": {"query": "star wars", "path": "title"}}},
                        {"$limit": 10},
                        {"$project": {"_id": 0, "title": 1, "score": {"$meta": "searchScore"}}},
                    ]
                )
            )

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
    search_tester = get_user_search_tester(mdb, use_ssl=True)
    movies_collection = search_tester.client["sample_mflix"]["movies"]

    # Get total document count
    total_docs = movies_collection.count_documents({})
    logger.info(f"Total documents in collection: {total_docs}")

    # Execute wildcard search to get all documents
    results = list(
        movies_collection.aggregate(
            [
                {
                    "$search": {
                        "index": "default",
                        "wildcard": {"query": "*", "path": "title", "allowAnalyzedField": True},
                    }
                },
                {"$project": {"_id": 0, "title": 1}},
            ]
        )
    )

    search_count = len(results)
    logger.info(f"Search through mongos returned {search_count} documents")

    # Verify search returns all documents (or close to it - some tolerance for timing)
    # TODO: verify 100% of documents, perhaps clone the movies collection to have sharded and unsharded queries to compare
    assert search_count > 0, "Search returned no results"
    assert (
        search_count >= total_docs * 0.9
    ), f"Search returned {search_count} but collection has {total_docs} (expected >= 90%)"

    logger.info(f"Search results verified: {search_count}/{total_docs} documents from all shards")


def get_admin_search_tester(mdb: MongoDB, use_ssl: bool = False) -> SearchTester:
    """Get SearchTester with admin credentials."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_sharded(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=use_ssl, ca_path=ca_path)


def get_user_search_tester(mdb: MongoDB, use_ssl: bool = False) -> SearchTester:
    """Get SearchTester with regular user credentials."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_sharded(mdb, USER_NAME, USER_PASSWORD, use_ssl=use_ssl, ca_path=ca_path)


def create_per_shard_search_tls_certs(namespace: str, issuer: str, prefix: str):
    """
    Create per-shard TLS certificates for MongoDBSearch resource.

    For each shard, creates a certificate with DNS names for:
    - The mongot service: {search-name}-mongot-{shardName}-svc.{namespace}.svc.cluster.local
    - The proxy service: {search-name}-mongot-{shardName}-proxy-svc.{namespace}.svc.cluster.local

    Secret naming pattern: {prefix}-{shardName}-search-cert
    e.g., certs-mdb-sh-0-search-cert, certs-mdb-sh-1-search-cert
    """
    logger.info(f"Creating per-shard Search TLS certificates with prefix '{prefix}'...")

    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        secret_name = f"{prefix}-{shard_name}-search-cert"

        # DNS names for this shard's mongot
        mongot_svc = f"{MDBS_RESOURCE_NAME}-mongot-{shard_name}-svc"
        proxy_svc = f"{MDBS_RESOURCE_NAME}-mongot-{shard_name}-proxy-svc"

        additional_domains = [
            f"{mongot_svc}.{namespace}.svc.cluster.local",
            f"{proxy_svc}.{namespace}.svc.cluster.local",
        ]

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=f"{shard_name}-search",
            secret_name=secret_name,
            additional_domains=additional_domains,
        )
        logger.info(f"Per-shard Search TLS certificate created: {secret_name}")

    logger.info(f"All {SHARD_COUNT} per-shard Search TLS certificates created")
