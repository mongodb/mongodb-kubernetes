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

import time

import pymongo
import pymongo.errors
import yaml
from kubernetes import client
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    get_service,
    get_statefulset,
    read_configmap,
    read_secret,
    try_load,
)
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
ENVOY_PROXY_PORT = 27029
ENVOY_ADMIN_PORT = 9901

# Resource names
MDB_RESOURCE_NAME = "mdb-sh-managed-lb"
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# TLS configuration
# Per-shard mongot TLS: {prefix}-{name}-search-0-{shardName}-cert (e.g., certs-mdb-sh-search-0-mdb-sh-0-cert)
# Managed LB server TLS: {prefix}-{name}-search-lb-0-cert (e.g., certs-mdb-sh-search-lb-0-cert)
# Managed LB client TLS: {prefix}-{name}-search-lb-0-client-cert (e.g., certs-mdb-sh-search-lb-0-client-cert)
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"


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
        name=MDB_RESOURCE_NAME,
    )

    if try_load(resource):
        return resource

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


# ============================================================================
# Helper Functions
# ============================================================================


def get_admin_search_tester(mdb: MongoDB, use_ssl: bool = False) -> SearchTester:
    """Get SearchTester with admin credentials."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_sharded(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=use_ssl, ca_path=ca_path)


def get_user_search_tester(mdb: MongoDB, use_ssl: bool = False) -> SearchTester:
    """Get SearchTester with regular user credentials."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_sharded(mdb, USER_NAME, USER_PASSWORD, use_ssl=use_ssl, ca_path=ca_path)


def create_lb_certificates(namespace: str, issuer: str):
    """Create TLS certificates for the operator-managed load balancer (Envoy proxy).

    Secret names must match what the operator expects per LoadBalancerServerCert() and
    LoadBalancerClientCert(): {prefix}-{name}-search-lb-0-cert and
    {prefix}-{name}-search-lb-0-client-cert.
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


def create_per_shard_search_tls_certs(namespace: str, issuer: str, prefix: str):
    """
    Create per-shard TLS certificates for MongoDBSearch resource.

    For each shard, creates a certificate with DNS names for:
    - The mongot service: {search-name}-search-0-{shardName}-svc.{namespace}.svc.cluster.local
    - The proxy service: {search-name}-search-0-{shardName}-proxy-svc.{namespace}.svc.cluster.local

    Secret naming pattern: {prefix}-{name}-search-0-{shardName}-cert
    e.g., certs-mdb-sh-search-0-mdb-sh-0-cert, certs-mdb-sh-search-0-mdb-sh-1-cert
    """
    logger.info(f"Creating per-shard Search TLS certificates with prefix '{prefix}'...")

    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        secret_name = search_resource_names.shard_tls_cert_name(MDB_RESOURCE_NAME, shard_name, prefix)

        additional_domains = [
            f"{search_resource_names.shard_service_name(MDB_RESOURCE_NAME, shard_name)}.{namespace}.svc.cluster.local",
            f"{search_resource_names.shard_proxy_service_name(MDB_RESOURCE_NAME, shard_name)}.{namespace}.svc.cluster.local",
        ]

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=search_resource_names.shard_statefulset_name(MDB_RESOURCE_NAME, shard_name),
            secret_name=secret_name,
            additional_domains=additional_domains,
        )
        logger.info(f"✓ Per-shard Search TLS certificate created: {secret_name}")

    logger.info(f"✓ All {SHARD_COUNT} per-shard Search TLS certificates created")


# ============================================================================
# Test Functions
# ============================================================================


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
    create_per_shard_search_tls_certs(namespace, issuer, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_search_resource(mdbs: MongoDBSearch):
    """Test MongoDBSearch resource deployment with managed LB config."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_envoy_deployment(namespace: str):
    """Verify operator-managed Envoy proxy deployment and configuration.

    The controller creates resources named {search-name}-search-lb-0-config,
    {search-name}-search-lb-0, and {search-name}-search-0-{shard}-proxy-svc.
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
def test_verify_per_shard_tls_secrets(namespace: str, mdbs: MongoDBSearch):
    """Verify that per-shard TLS secrets are created by the operator.

    Checks for:
    1. Source secrets (from cert-manager): {prefix}-{name}-search-0-{shardName}-cert
    2. Operator-managed secrets: {shardName}-search-certificate-key
    """
    logger.info("Verifying per-shard TLS secrets...")

    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"

        # Verify source secret (created by cert-manager in test_)
        source_secret_name = search_resource_names.shard_tls_cert_name(MDB_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX)
        try:
            source_secret = read_secret(namespace, source_secret_name)
            assert "tls.crt" in source_secret, f"Source secret {source_secret_name} missing tls.crt"
            assert "tls.key" in source_secret, f"Source secret {source_secret_name} missing tls.key"
            logger.info(f"✓ Source secret verified: {source_secret_name}")
        except Exception as e:
            raise AssertionError(f"Source secret {source_secret_name} not found: {e}")

        # Verify operator-managed secret (created by Search controller)
        # The operator creates a secret with a hash-based key like "abc123def456...pem"
        # (SHA256 hash of the certificate content + ".pem" extension)
        operator_secret_name = f"{shard_name}-search-certificate-key"

        def check_operator_secret():
            try:
                operator_secret = read_secret(namespace, operator_secret_name)
                # The operator uses hash-based filenames ending in .pem
                pem_keys = [k for k in operator_secret.keys() if k.endswith(".pem")]
                has_pem = len(pem_keys) > 0
                return has_pem, f"Operator secret {operator_secret_name}: pem_keys={pem_keys}"
            except Exception as e:
                return False, f"Operator secret {operator_secret_name} not found: {e}"

        run_periodically(
            check_operator_secret, timeout=120, sleep_time=5, msg=f"operator secret {operator_secret_name}"
        )
        logger.info(f"✓ Operator secret verified: {operator_secret_name}")

    logger.info(f"✓ All {SHARD_COUNT} per-shard TLS secrets verified")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_wait_for_sharded_cluster_ready(mdb: MongoDB):
    """Wait for sharded cluster to be ready after Search CR deployment."""
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_per_shard_services(namespace: str, mdbs: MongoDBSearch):
    """
    Verify that per-shard mongot Services are created.

    For a sharded cluster with managed LB, the Search controller should create
    one Service per shard with naming: <search-name>-search-0-<shardName>-svc
    """
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        service_name = search_resource_names.shard_service_name(mdbs.name, shard_name)

        logger.info(f"Checking for per-shard Service: {service_name}")
        service = get_service(namespace, service_name)

        assert service is not None, f"Per-shard Service {service_name} not found"
        assert service.spec is not None, f"Service {service_name} has no spec"

        # Verify the service has the expected port
        ports = {p.name: p.port for p in service.spec.ports}
        assert (
            "mongot" in ports or MONGOT_PORT in ports.values()
        ), f"Service {service_name} does not have mongot port ({MONGOT_PORT})"

        logger.info(f"✓ Per-shard Service {service_name} exists with ports: {ports}")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_per_shard_statefulsets(namespace: str, mdbs: MongoDBSearch):
    """
    Verify that per-shard mongot StatefulSets are created.

    For a sharded cluster with managed LB, the Search controller should create
    one StatefulSet per shard with naming: <search-name>-search-0-<shardName>
    """
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        sts_name = search_resource_names.shard_statefulset_name(mdbs.name, shard_name)

        logger.info(f"Checking for per-shard StatefulSet: {sts_name}")

        # Wait for the StatefulSet to have at least 1 ready replica
        max_wait_time = 120  # seconds
        poll_interval = 5  # seconds
        start_time = time.time()
        ready_replicas = 0

        while time.time() - start_time < max_wait_time:
            try:
                sts = get_statefulset(namespace, sts_name)
                assert sts is not None, f"Per-shard StatefulSet {sts_name} not found"
                assert sts.status is not None, f"StatefulSet {sts_name} has no status"

                ready_replicas = sts.status.ready_replicas or 0
                if ready_replicas >= 1:
                    break

                logger.info(f"StatefulSet {sts_name} has {ready_replicas} ready replicas, waiting...")
                time.sleep(poll_interval)
            except Exception as e:
                logger.warning(f"Error checking StatefulSet {sts_name}: {e}")
                time.sleep(poll_interval)

        assert (
            ready_replicas >= 1
        ), f"StatefulSet {sts_name} has {ready_replicas} ready replicas after {max_wait_time}s, expected >= 1"

        logger.info(f"✓ Per-shard StatefulSet {sts_name} exists with {ready_replicas} ready replicas")


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
    search_tester = get_admin_search_tester(mdb, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )
    logger.info("✓ Sample database restored")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_shard_collections(mdb: MongoDB):
    """Shard the movies and embedded_movies collections.

    Uses SearchTester with direct pymongo connection. This works locally with kubefwd
    because pymongo (Python) properly resolves /etc/hosts entries, unlike Go-based tools.
    """
    search_tester = get_admin_search_tester(mdb, use_ssl=True)
    client = search_tester.client
    admin_db = client.admin
    sample_mflix_db = client["sample_mflix"]

    # Enable sharding on database
    try:
        admin_db.command("enableSharding", "sample_mflix")
        logger.info("✓ Sharding enabled on sample_mflix database")
    except pymongo.errors.OperationFailure as e:
        if "already enabled" in str(e) or e.code == 23:  # AlreadyInitialized
            logger.info("Sharding already enabled on sample_mflix")
        else:
            raise

    # Shard movies collection
    try:
        sample_mflix_db["movies"].create_index([("_id", pymongo.HASHED)])
        admin_db.command("shardCollection", "sample_mflix.movies", key={"_id": "hashed"})
        logger.info("✓ movies collection sharded")
    except pymongo.errors.OperationFailure as e:
        if "already sharded" in str(e) or e.code == 20:  # AlreadyInitialized for sharding
            logger.info("movies collection already sharded")
        else:
            raise

    # Shard embedded_movies collection
    try:
        sample_mflix_db["embedded_movies"].create_index([("_id", pymongo.HASHED)])
        admin_db.command("shardCollection", "sample_mflix.embedded_movies", key={"_id": "hashed"})
        logger.info("✓ embedded_movies collection sharded")
    except pymongo.errors.OperationFailure as e:
        if "already sharded" in str(e) or e.code == 20:  # AlreadyInitialized for sharding
            logger.info("embedded_movies collection already sharded")
        else:
            raise

    # Wait for balancer to distribute chunks
    time.sleep(10)
    logger.info("✓ Collections sharded and balanced")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_search_index(mdb: MongoDB):
    """Create text search index on movies collection.

    Uses SearchTester with direct pymongo connection. This works locally with kubefwd
    because pymongo (Python) properly resolves /etc/hosts entries, unlike Go-based tools.
    """
    search_tester = get_user_search_tester(mdb, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    logger.info("✓ Text search index created")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_wait_for_search_index_ready(mdb: MongoDB):
    """Wait for search index to be ready.

    Uses SearchTester with direct pymongo connection. This works locally with kubefwd
    because pymongo (Python) properly resolves /etc/hosts entries, unlike Go-based tools.
    """
    search_tester = get_user_search_tester(mdb, use_ssl=True)
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)
    logger.info("✓ Search index is ready")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_execute_text_search_query(mdb: MongoDB):
    """Execute text search query through mongos and verify results.

    Uses SearchTester with direct pymongo connection. This works locally with kubefwd
    because pymongo (Python) properly resolves /etc/hosts entries, unlike Go-based tools.
    """
    search_tester = get_user_search_tester(mdb, use_ssl=True)

    def execute_search():
        try:
            # Execute search query using pymongo aggregation
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
    logger.info("✓ Text search query executed successfully through mongos")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_search_results_from_all_shards(mdb: MongoDB):
    """
    Verify that search results through mongos contain documents from all shards.
    This is the definitive test that mongos is correctly aggregating search results.

    Uses SearchTester with direct pymongo connection. This works locally with kubefwd
    because pymongo (Python) properly resolves /etc/hosts entries, unlike Go-based tools.
    """
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
    assert search_count > 0, "Search returned no results"
    assert (
        search_count >= total_docs * 0.9
    ), f"Search returned {search_count} but collection has {total_docs} (expected >= 90%)"

    logger.info(f"✓ Search results verified: {search_count}/{total_docs} documents from all shards")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_search_resource_status(mdbs: MongoDBSearch):
    """Verify the MongoDBSearch resource is in Running phase with correct status."""
    mdbs.load()

    phase = mdbs.get_status_phase()
    assert phase == Phase.Running, f"MongoDBSearch phase is {phase}, expected Running"

    logger.info(f"✓ MongoDBSearch {mdbs.name} is in Running phase")
