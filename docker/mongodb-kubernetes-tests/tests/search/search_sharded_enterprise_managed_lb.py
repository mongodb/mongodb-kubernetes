"""
E2E test for sharded MongoDB Search with managed L7 load balancer configuration.

This test verifies the sharded Search + managed LB PoC implementation:
- Deploys a sharded MongoDB cluster with TLS enabled
- Deploys MongoDBSearch with lb.mode: Managed (operator auto-deploys Envoy proxy)
- Verifies the operator-managed Envoy proxy deployment and configuration
- Verifies per-shard mongot Services are created
- Verifies per-shard mongot StatefulSets are created
- Verifies mongod parameters are set correctly for each shard (pointing to Envoy proxy)
- Verifies mongos search parameters are configured
- Imports sample data and shards collections
- Creates text and vector search indexes
- Executes search queries through mongos and verifies results from all shards
"""

import pymongo
import pymongo.errors
import yaml
from kubernetes import client
from kubetester import create_or_update_secret, get_service, read_configmap, try_load
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
from tests.common.search import search_resource_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.sharded_search_helper import *
from tests.conftest import get_default_operator
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
ENVOY_PROXY_PORT = 27029
ENVOY_ADMIN_PORT = 9901

# Resource names
MDB_RESOURCE_NAME = "mdb-sh-managed-lb"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# TLS configuration
# Per-shard mongot TLS: {prefix}-{name}-search-0-{shardName}-cert (e.g., certs-mdb-sh-search-0-mdb-sh-0-cert)
# Managed LB server TLS: {prefix}-{name}-search-lb-cert (e.g., certs-mdb-sh-search-lb-cert)
# Managed LB client TLS: {prefix}-{name}-search-lb-client-cert (e.g., certs-mdb-sh-search-lb-client-cert)
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_sharded_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def mdb(namespace: str, sharded_ca_configmap: str) -> MongoDB:
    """Fixture for sharded MongoDB cluster with TLS enabled."""
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-sharded-cluster-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    # Configure OpsManager/CloudManager connection
    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)

    resource["spec"]["security"]["tls"]["ca"] = CA_CONFIGMAP_NAME

    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    """Fixture for MongoDBSearch with managed LB configuration.

    Uses per-shard TLS with certsSecretPrefix and lb.mode: Managed.
    The operator automatically deploys and configures the Envoy proxy.
    """
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-sharded-managed-lb.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )

    if try_load(resource):
        return resource

    resource["spec"]["source"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME

    return resource


@fixture(scope="function")
def admin_user(namespace: str) -> MongoDBUser:
    return make_admin_user(namespace, MDB_RESOURCE_NAME, ADMIN_USER_NAME)


@fixture(scope="function")
def user(namespace: str) -> MongoDBUser:
    return make_user(namespace, MDB_RESOURCE_NAME, USER_NAME)


@fixture(scope="function")
def mongot_user(namespace: str, mdbs: MongoDBSearch) -> MongoDBUser:
    return make_mongot_user(namespace, mdbs, MDB_RESOURCE_NAME, MONGOT_USER_NAME)


def create_lb_certificates(namespace: str, issuer: str):
    """Create TLS certificates for the operator-managed load balancer (Envoy proxy).

    Secret names must match what the operator expects per LoadBalancerServerCert() and
    LoadBalancerClientCert(): {prefix}-{name}-search-lb-cert and
    {prefix}-{name}-search-lb-client-cert.
    """
    logger.info("Creating managed LB certificates...")

    lb_server_cert_name = search_resource_names.lb_server_cert_name(MDB_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)
    lb_client_cert_name = search_resource_names.lb_client_cert_name(MDB_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)

    # Build SANs for server certificate (all per-shard proxy services)
    additional_domains = []
    for i in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{i}"
        proxy_svc = search_resource_names.shard_proxy_service_name(MDB_RESOURCE_NAME, shard_name)
        additional_domains.append(f"{proxy_svc}.{namespace}.svc.cluster.local")

    # Add wildcard for flexibility
    additional_domains.append(f"*.{namespace}.svc.cluster.local")

    # Create server certificate
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.lb_deployment_name(MDB_RESOURCE_NAME),
        replicas=1,
        service_name=search_resource_names.lb_service_name(MDB_RESOURCE_NAME),
        additional_domains=additional_domains,
        secret_name=lb_server_cert_name,
    )
    logger.info(f"✓ LB server certificate created: {lb_server_cert_name}")

    # Create client certificate
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=f"{search_resource_names.lb_deployment_name(MDB_RESOURCE_NAME)}-client",
        replicas=1,
        service_name=search_resource_names.lb_service_name(MDB_RESOURCE_NAME),
        additional_domains=[f"*.{namespace}.svc.cluster.local"],
        secret_name=lb_client_cert_name,
    )
    logger.info(f"✓ LB client certificate created: {lb_client_cert_name}")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    """Test that the operator is installed and running."""
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_sharded_enterprise_managed_lb
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    """Test OpsManager deployment (skipped for Cloud Manager)."""
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_install_tls_certificates(namespace: str, mdb: MongoDB, issuer: str):
    """Install TLS certificates for sharded cluster."""
    # Note: secret_prefix must include trailing hyphen to match the operator's expected format
    # Operator generates: {certsSecretPrefix}-{resource_name}-{shard_idx}-cert
    # e.g., mdb-sh-mdb-sh-0-cert
    #
    # IMPORTANT: For Search with sharded clusters, the mongot needs to connect to the mongos
    # service (mdb-sh-svc.mongodb-test.svc.cluster.local). The certificate must include this
    # service DNS name as a Subject Alternative Name (SAN).
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
    logger.info("✓ Sharded cluster TLS certificates created")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_sharded_cluster(mdb: MongoDB):
    """Test sharded cluster deployment."""
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_sharded_enterprise_managed_lb
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
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace,
        name=user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": USER_PASSWORD},
    )
    user.update()
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace,
        name=mongot_user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": MONGOT_USER_PASSWORD},
    )
    mongot_user.update()
    # Don't wait for mongot user - it needs searchCoordinator role from Search CR


@mark.e2e_search_sharded_enterprise_managed_lb
def test_deploy_lb_certificates(namespace: str, issuer: str):
    """Create TLS certificates for the operator-managed load balancer."""
    create_lb_certificates(namespace, issuer)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_search_tls_certificate(namespace: str, issuer: str):
    """Create per-shard TLS certificates for MongoDBSearch resource.

    Creates one certificate per shard with naming pattern:
    {prefix}-{name}-search-0-{shardName}-cert (e.g., certs-mdb-sh-search-0-mdb-sh-0-cert)

    The operator will create operator-managed secrets:
    {shardName}-search-certificate-key (e.g., mdb-sh-0-search-certificate-key)
    """
    create_per_shard_search_tls_certs(
        namespace, issuer, MDBS_TLS_CERT_PREFIX, SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME
    )


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_search_resource(mdbs: MongoDBSearch):
    """Test MongoDBSearch resource deployment with managed LB config."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_envoy_deployment(namespace: str):
    """Verify operator-managed Envoy proxy deployment and configuration.

    The controller creates resources named {search-name}-search-lb-config,
    {search-name}-search-lb, and {search-name}-search-0-{shard}-proxy-svc.
    Uses polling since the controller creates these asynchronously after the
    MongoDBSearch CR reaches Running phase.
    """
    envoy_config_name = search_resource_names.lb_configmap_name(MDB_RESOURCE_NAME)
    envoy_deployment_name = search_resource_names.lb_deployment_name(MDB_RESOURCE_NAME)

    # Verify Envoy ConfigMap exists (with polling)
    def check_envoy_configmap():
        try:
            config = read_configmap(namespace, envoy_config_name)
            has_yaml = "envoy.yaml" in config
            has_listener = has_yaml and "mongod_listener" in config["envoy.yaml"]
            return has_yaml and has_listener, f"has_yaml={has_yaml}, has_listener={has_listener}"
        except Exception as e:
            return False, f"ConfigMap {envoy_config_name} not found: {e}"

    run_periodically(check_envoy_configmap, timeout=120, sleep_time=5, msg=f"Envoy ConfigMap {envoy_config_name}")
    logger.info(f"✓ Envoy ConfigMap {envoy_config_name} verified")

    # Verify Envoy Deployment is running (with polling)
    def check_envoy_deployment():
        try:
            apps_v1 = client.AppsV1Api()
            deployment = apps_v1.read_namespaced_deployment(envoy_deployment_name, namespace)
            ready = deployment.status.ready_replicas or 0
            return ready >= 1, f"ready_replicas={ready}"
        except Exception as e:
            return False, f"Deployment {envoy_deployment_name} not found: {e}"

    run_periodically(check_envoy_deployment, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_deployment_name}")
    logger.info(f"✓ Envoy Deployment {envoy_deployment_name} is running")

    # Verify per-shard proxy Services exist
    for i in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{i}"
        proxy_svc_name = search_resource_names.shard_proxy_service_name(MDB_RESOURCE_NAME, shard_name)

        def check_proxy_service(svc_name=proxy_svc_name):
            try:
                service = get_service(namespace, svc_name)
                if service is None:
                    return False, f"Proxy Service {svc_name} not found"
                ports = {p.port for p in service.spec.ports}
                has_port = ENVOY_PROXY_PORT in ports
                return has_port, f"ports={ports}, has_proxy_port={has_port}"
            except Exception as e:
                return False, f"Proxy Service {svc_name} not found: {e}"

        run_periodically(check_proxy_service, timeout=120, sleep_time=5, msg=f"Proxy Service {proxy_svc_name}")
        logger.info(f"✓ Proxy Service {proxy_svc_name} verified")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_wait_for_sharded_cluster_ready(mdb: MongoDB):
    """Wait for sharded cluster to be ready after Search CR deployment."""
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_mongod_parameters_per_shard(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    """
    Verify that each shard's mongod has the correct search parameters.

    For sharded clusters with managed LB (operator-managed Envoy proxy), each shard should have:
    - mongotHost pointing to its shard-specific Envoy proxy endpoint (port 27029)
    - searchIndexManagementHostAndPort pointing to the same endpoint
    """

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

                set_parameter = mongod_config.get("setParameter", {})
                mongot_host = set_parameter.get("mongotHost", "")
                search_mgmt_host = set_parameter.get("searchIndexManagementHostAndPort", "")

                # For managed LB mode, endpoint should point to proxy service on port 27029
                expected_proxy_service = search_resource_names.shard_proxy_service_name(mdbs.name, shard_name)

                if expected_proxy_service not in mongot_host:
                    all_correct = False
                    status_msgs.append(f"Shard {shard_name}: mongotHost missing {expected_proxy_service}")
                elif str(ENVOY_PROXY_PORT) not in mongot_host:
                    all_correct = False
                    status_msgs.append(f"Shard {shard_name}: mongotHost missing port {ENVOY_PROXY_PORT}")
                else:
                    status_msgs.append(f"Shard {shard_name}: ✓ mongotHost={mongot_host}")

            except Exception as e:
                all_correct = False
                status_msgs.append(f"Shard {shard_name}: Error - {e}")

        return all_correct, "\n".join(status_msgs)

    run_periodically(check_mongod_parameters, timeout=300, sleep_time=10, msg="mongod search parameters")
    logger.info("✓ All shards have correct mongod search parameters pointing to Envoy proxy")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_mongos_search_config(namespace: str, mdb: MongoDB):
    """
    Verify that mongos has the correct search parameters configured.

    Mongos should have:
    - mongotHost configured
    - searchIndexManagementHostAndPort configured
    - useGrpcForSearch: true
    """
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
    logger.info("✓ Mongos has correct search configuration")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    """Deploy mongodb-tools pod for running queries."""
    # The tools_pod fixture handles deployment and waiting for readiness
    logger.info(f"✓ Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    """Restore sample_mflix database to the sharded cluster.

    Uses mongorestore from inside the tools pod since the MongoDB cluster
    is only accessible via Kubernetes internal DNS.
    """
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )
    logger.info("✓ Sample database restored")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_search_shard_collections(mdb: MongoDB):
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.shard_and_distribute_collection("sample_mflix", "movies")
    logger.info("Collections sharded and chunks are distributed")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_search_index(mdb: MongoDB):
    """Create text search index on movies collection.

    Uses SearchTester with direct pymongo connection. This works locally with kubefwd
    because pymongo (Python) properly resolves /etc/hosts entries, unlike Go-based tools.
    """
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    logger.info("✓ Text search index created")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_wait_for_search_index_ready(mdb: MongoDB):
    """Wait for search index to be ready.

    Uses SearchTester with direct pymongo connection. This works locally with kubefwd
    because pymongo (Python) properly resolves /etc/hosts entries, unlike Go-based tools.
    """
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)
    logger.info("✓ Search index is ready")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_execute_text_search_query(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
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


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_search_results_from_all_shards(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
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


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_search_resource_status(mdbs: MongoDBSearch):
    """Verify the MongoDBSearch resource is in Running phase with correct status."""
    mdbs.load()

    phase = mdbs.get_status_phase()
    assert phase == Phase.Running, f"MongoDBSearch phase is {phase}, expected Running"

    logger.info(f"✓ MongoDBSearch {mdbs.name} is in Running phase")
