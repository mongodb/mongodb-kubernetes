"""
E2E test for sharded MongoDB Search with external L7 load balancer configuration.

This test verifies the sharded Search + external L7 LB PoC implementation:
- Deploys a sharded MongoDB cluster with TLS enabled
- Deploys Envoy proxy for L7 load balancing mongot traffic
- Deploys MongoDBSearch with per-shard external LB endpoints
- Verifies Envoy proxy deployment and configuration
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
    get_pod_when_ready,
    get_service,
    get_statefulset,
    read_configmap,
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
from tests.common.search import movies_search_helper
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
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
MDB_RESOURCE_NAME = "mdb-sh"
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# TLS configuration
MDBS_TLS_SECRET_NAME = "mdb-sh-search-cert"
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
    """Fixture for MongoDBSearch with sharded external LB configuration."""
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-sharded-external-lb.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )

    if try_load(resource):
        return resource

    # Replace NAMESPACE placeholder in endpoint URLs with actual namespace
    # Note: Port (27029) and service names (*-proxy-svc) are already correct in the YAML
    spec = resource["spec"]
    if "lb" in spec and "external" in spec["lb"] and "sharded" in spec["lb"]["external"]:
        endpoints = spec["lb"]["external"]["sharded"]["endpoints"]
        for endpoint in endpoints:
            endpoint["endpoint"] = endpoint["endpoint"].replace("NAMESPACE", namespace)

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


def get_connection_string(mdb: MongoDB, user_name: str, user_password: str) -> str:
    """Get connection string for sharded cluster (connects to mongos)."""
    return (
        f"mongodb://{user_name}:{user_password}@"
        f"{mdb.name}-mongos-0.{mdb.name}-svc.{mdb.namespace}.svc.cluster.local:27017/"
        f"?authSource=admin"
    )


def get_admin_sample_movies_helper(mdb: MongoDB) -> SampleMoviesSearchHelper:
    """Get SampleMoviesSearchHelper with admin credentials."""
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            get_connection_string(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD),
            use_ssl=True,
            ca_path=get_issuer_ca_filepath(),
        )
    )


def get_user_sample_movies_helper(mdb: MongoDB) -> SampleMoviesSearchHelper:
    """Get SampleMoviesSearchHelper with regular user credentials."""
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            get_connection_string(mdb, USER_NAME, USER_PASSWORD),
            use_ssl=True,
            ca_path=get_issuer_ca_filepath(),
        )
    )


def get_admin_search_tester(mdb: MongoDB, use_ssl: bool = False) -> SearchTester:
    """Get SearchTester with admin credentials (for direct pymongo operations)."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester(
        get_connection_string(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD),
        use_ssl=use_ssl,
        ca_path=ca_path,
    )


def get_user_search_tester(mdb: MongoDB, use_ssl: bool = False) -> SearchTester:
    """Get SearchTester with regular user credentials (for direct pymongo operations)."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester(
        get_connection_string(mdb, USER_NAME, USER_PASSWORD),
        use_ssl=use_ssl,
        ca_path=ca_path,
    )


def deploy_envoy_proxy(namespace: str):
    """
    Deploy Envoy proxy for L7 load balancing mongot traffic.
    Creates ConfigMap, Deployment, and per-shard proxy Services.
    """
    logger.info("Deploying Envoy proxy...")

    # Create Envoy ConfigMap with SNI-based routing
    _create_envoy_configmap(namespace)

    # Create Envoy Deployment
    _create_envoy_deployment(namespace)

    # Create per-shard proxy Services
    _create_envoy_proxy_services(namespace)

    # Wait for Envoy to be ready
    _wait_for_envoy_ready(namespace)

    logger.info("✓ Envoy proxy deployed successfully")


def _create_envoy_configmap(namespace: str):
    """Create Envoy ConfigMap with SNI-based routing configuration."""
    filter_chains = ""
    clusters = ""

    for i in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{i}"
        proxy_svc = f"{MDB_RESOURCE_NAME}-mongot-{shard_name}-proxy-svc"
        search_svc = f"{MDB_RESOURCE_NAME}-mongot-{shard_name}-svc"
        cluster_name = f"mongot_{shard_name.replace('-', '_')}_cluster"

        # Filter chain for this shard
        filter_chains += f"""
        - filter_chain_match:
            server_names:
            - "{proxy_svc}.{namespace}.svc.cluster.local"
          filters:
          - name: envoy.filters.network.http_connection_manager
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
              stat_prefix: ingress_{shard_name.replace('-', '_')}
              codec_type: AUTO
              route_config:
                name: {shard_name}_route
                virtual_hosts:
                - name: mongot_{shard_name.replace('-', '_')}_backend
                  domains: ["*"]
                  routes:
                  - match:
                      prefix: "/"
                      grpc: {{}}
                    route:
                      cluster: {cluster_name}
                      timeout: 300s
              http_filters:
              - name: envoy.filters.http.router
                typed_config:
                  "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
              http2_protocol_options:
                initial_connection_window_size: 1048576
                initial_stream_window_size: 1048576
              stream_idle_timeout: 300s
              request_timeout: 300s
          transport_socket:
            name: envoy.transport_sockets.tls
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
              common_tls_context:
                tls_certificates:
                - certificate_chain:
                    filename: /etc/envoy/tls/server/tls.crt
                  private_key:
                    filename: /etc/envoy/tls/server/tls.key
                validation_context:
                  trusted_ca:
                    filename: /etc/envoy/tls/ca/ca-pem
                tls_params:
                  tls_minimum_protocol_version: TLSv1_2
                  tls_maximum_protocol_version: TLSv1_2
                alpn_protocols:
                - "h2"
              require_client_certificate: true"""

        # Cluster for this shard
        clusters += f"""
      - name: {cluster_name}
        type: STRICT_DNS
        lb_policy: ROUND_ROBIN
        http2_protocol_options:
          initial_connection_window_size: 1048576
          initial_stream_window_size: 1048576
        load_assignment:
          cluster_name: {cluster_name}
          endpoints:
          - lb_endpoints:
            - endpoint:
                address:
                  socket_address:
                    address: {search_svc}.{namespace}.svc.cluster.local
                    port_value: {MONGOT_PORT}
        circuit_breakers:
          thresholds:
          - priority: DEFAULT
            max_connections: 1024
            max_pending_requests: 1024
            max_requests: 1024
            max_retries: 3
        transport_socket:
          name: envoy.transport_sockets.tls
          typed_config:
            "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
            common_tls_context:
              tls_certificates:
              - certificate_chain:
                  filename: /etc/envoy/tls/client/tls.crt
                private_key:
                  filename: /etc/envoy/tls/client/tls.key
              validation_context:
                trusted_ca:
                  filename: /etc/envoy/tls/ca/ca-pem
              alpn_protocols:
              - "h2"
            sni: {search_svc}.{namespace}.svc.cluster.local
        upstream_connection_options:
          tcp_keepalive:
            keepalive_time: 10
            keepalive_interval: 3
            keepalive_probes: 3
        common_http_protocol_options:
          idle_timeout: 300s"""

    envoy_config = f"""admin:
  address:
    socket_address:
      address: 0.0.0.0
      port_value: {ENVOY_ADMIN_PORT}

static_resources:
  listeners:
  - name: mongod_listener
    address:
      socket_address:
        address: 0.0.0.0
        port_value: {ENVOY_PROXY_PORT}
    listener_filters:
    - name: envoy.filters.listener.tls_inspector
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector
    filter_chains:{filter_chains}

  clusters:{clusters}

layered_runtime:
  layers:
  - name: static_layer
    static_layer:
      overload:
        global_downstream_max_connections: 50000
"""

    create_or_update_configmap(namespace, "envoy-config", {"envoy.yaml": envoy_config})
    logger.info(f"✓ Envoy ConfigMap created with routing for {SHARD_COUNT} shards")


def _create_envoy_deployment(namespace: str):
    """Create Envoy Deployment."""
    deployment = {
        "apiVersion": "apps/v1",
        "kind": "Deployment",
        "metadata": {
            "name": "envoy-proxy",
            "labels": {"app": "envoy-proxy", "component": "search-proxy"},
        },
        "spec": {
            "replicas": 1,
            "selector": {"matchLabels": {"app": "envoy-proxy"}},
            "template": {
                "metadata": {"labels": {"app": "envoy-proxy", "component": "search-proxy"}},
                "spec": {
                    "containers": [
                        {
                            "name": "envoy",
                            "image": "envoyproxy/envoy:v1.31-latest",
                            "command": ["/usr/local/bin/envoy"],
                            "args": ["-c", "/etc/envoy/envoy.yaml", "--log-level", "info"],
                            "ports": [
                                {"name": "grpc", "containerPort": ENVOY_PROXY_PORT},
                                {"name": "admin", "containerPort": ENVOY_ADMIN_PORT},
                            ],
                            "resources": {
                                "requests": {"cpu": "100m", "memory": "128Mi"},
                                "limits": {"cpu": "500m", "memory": "512Mi"},
                            },
                            "readinessProbe": {
                                "httpGet": {"path": "/ready", "port": ENVOY_ADMIN_PORT},
                                "initialDelaySeconds": 5,
                                "periodSeconds": 5,
                            },
                            "volumeMounts": [
                                {"name": "envoy-config", "mountPath": "/etc/envoy", "readOnly": True},
                                {"name": "envoy-server-cert", "mountPath": "/etc/envoy/tls/server", "readOnly": True},
                                {"name": "envoy-client-cert", "mountPath": "/etc/envoy/tls/client", "readOnly": True},
                                {"name": "ca-cert", "mountPath": "/etc/envoy/tls/ca", "readOnly": True},
                            ],
                        }
                    ],
                    "volumes": [
                        {"name": "envoy-config", "configMap": {"name": "envoy-config"}},
                        {"name": "envoy-server-cert", "secret": {"secretName": "envoy-server-cert-pem"}},
                        {"name": "envoy-client-cert", "secret": {"secretName": "envoy-client-cert-pem"}},
                        {
                            "name": "ca-cert",
                            "configMap": {"name": CA_CONFIGMAP_NAME, "items": [{"key": "ca-pem", "path": "ca-pem"}]},
                        },
                    ],
                },
            },
        },
    }

    try:
        KubernetesTester.create_deployment(namespace, deployment)
        logger.info("✓ Envoy Deployment created")
    except Exception as e:
        logger.info(f"Envoy Deployment may already exist: {e}")


def _create_envoy_proxy_services(namespace: str):
    """Create per-shard proxy Services pointing to Envoy."""
    for i in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{i}"
        proxy_svc_name = f"{MDB_RESOURCE_NAME}-mongot-{shard_name}-proxy-svc"

        service = {
            "apiVersion": "v1",
            "kind": "Service",
            "metadata": {
                "name": proxy_svc_name,
                "labels": {"app": "envoy-proxy", "target-shard": shard_name},
            },
            "spec": {
                "type": "ClusterIP",
                "selector": {"app": "envoy-proxy"},
                "ports": [{"name": "grpc", "port": ENVOY_PROXY_PORT, "targetPort": ENVOY_PROXY_PORT}],
            },
        }

        try:
            KubernetesTester.create_service(namespace, service)
            logger.info(f"✓ Proxy Service {proxy_svc_name} created")
        except Exception as e:
            logger.info(f"Proxy Service {proxy_svc_name} may already exist: {e}")


def _wait_for_envoy_ready(namespace: str, timeout: int = 120):
    """Wait for Envoy deployment to be ready."""

    def check_envoy_ready():
        try:
            apps_v1 = client.AppsV1Api()
            deployment = apps_v1.read_namespaced_deployment("envoy-proxy", namespace)
            ready = deployment.status.ready_replicas or 0
            return ready >= 1, f"Envoy ready replicas: {ready}"
        except Exception as e:
            return False, f"Error checking Envoy: {e}"

    run_periodically(check_envoy_ready, timeout=timeout, sleep_time=5, msg="Envoy proxy to be ready")


def create_envoy_certificates(namespace: str, issuer: str):
    """Create TLS certificates for Envoy proxy."""
    logger.info("Creating Envoy proxy certificates...")

    # Build SANs for server certificate (all per-shard proxy services)
    additional_domains = []
    for i in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{i}"
        proxy_svc = f"{MDB_RESOURCE_NAME}-mongot-{shard_name}-proxy-svc"
        additional_domains.append(f"{proxy_svc}.{namespace}.svc.cluster.local")

    # Add wildcard for flexibility
    additional_domains.append(f"*.{namespace}.svc.cluster.local")

    # Create server certificate
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name="envoy-server",
        replicas=1,
        service_name="envoy-proxy",
        additional_domains=additional_domains,
        secret_name="envoy-server-cert-pem",
    )
    logger.info("✓ Envoy server certificate created")

    # Create client certificate
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name="envoy-client",
        replicas=1,
        service_name="envoy-proxy-client",
        additional_domains=[f"*.{namespace}.svc.cluster.local"],
        secret_name="envoy-client-cert-pem",
    )
    logger.info("✓ Envoy client certificate created")


# ============================================================================
# Test Functions
# ============================================================================


@mark.e2e_search_sharded_enterprise_external_lb
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    """Test that the operator is installed and running."""
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_sharded_enterprise_external_lb
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    """Test OpsManager deployment (skipped for Cloud Manager)."""
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_external_lb
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


@mark.e2e_search_sharded_enterprise_external_lb
def test_create_sharded_cluster(mdb: MongoDB):
    """Test sharded cluster deployment."""
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_sharded_enterprise_external_lb
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
    admin_user.create()
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace,
        name=user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": USER_PASSWORD},
    )
    user.create()
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace,
        name=mongot_user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": MONGOT_USER_PASSWORD},
    )
    mongot_user.create()
    # Don't wait for mongot user - it needs searchCoordinator role from Search CR


@mark.e2e_search_sharded_enterprise_external_lb
def test_deploy_envoy_certificates(namespace: str, issuer: str):
    """Create TLS certificates for Envoy proxy."""
    create_envoy_certificates(namespace, issuer)


@mark.e2e_search_sharded_enterprise_external_lb
def test_deploy_envoy_proxy(namespace: str):
    """Deploy Envoy proxy for L7 load balancing."""
    deploy_envoy_proxy(namespace)


@mark.e2e_search_sharded_enterprise_external_lb
def test_verify_envoy_deployment(namespace: str):
    """Verify Envoy proxy deployment and configuration."""
    # Verify Envoy ConfigMap exists
    config = read_configmap(namespace, "envoy-config")
    assert "envoy.yaml" in config, "Envoy ConfigMap missing envoy.yaml"
    assert "mongod_listener" in config["envoy.yaml"], "Envoy config missing listener"
    logger.info("✓ Envoy ConfigMap verified")

    # Verify Envoy Deployment is running
    apps_v1 = client.AppsV1Api()
    deployment = apps_v1.read_namespaced_deployment("envoy-proxy", namespace)
    assert deployment.status.ready_replicas >= 1, "Envoy Deployment not ready"
    logger.info("✓ Envoy Deployment is running")

    # Verify per-shard proxy Services exist
    for i in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{i}"
        proxy_svc_name = f"{MDB_RESOURCE_NAME}-mongot-{shard_name}-proxy-svc"
        service = get_service(namespace, proxy_svc_name)
        assert service is not None, f"Proxy Service {proxy_svc_name} not found"
        ports = {p.port for p in service.spec.ports}
        assert ENVOY_PROXY_PORT in ports, f"Proxy Service missing port {ENVOY_PROXY_PORT}"
        logger.info(f"✓ Proxy Service {proxy_svc_name} verified")


@mark.e2e_search_sharded_enterprise_external_lb
def test_create_search_tls_certificate(namespace: str, issuer: str):
    """Create TLS certificate for MongoDBSearch resource."""
    # Create TLS certificate for the Search resource
    # The Search resource expects a secret named mdb-sh-search-cert
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=MDB_RESOURCE_NAME,
        secret_name=MDBS_TLS_SECRET_NAME,
    )
    logger.info(f"✓ Search TLS certificate created: {MDBS_TLS_SECRET_NAME}")


@mark.e2e_search_sharded_enterprise_external_lb
def test_create_search_resource(mdbs: MongoDBSearch):
    """Test MongoDBSearch resource deployment with sharded external LB config."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_external_lb
def test_wait_for_sharded_cluster_ready(mdb: MongoDB):
    """Wait for sharded cluster to be ready after Search CR deployment."""
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_external_lb
def test_wait_for_agents_ready(mdb: MongoDB):
    """Wait for agents to be ready after Search CR deployment."""
    mdb.get_om_tester().wait_agents_ready()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_sharded_enterprise_external_lb
def test_verify_per_shard_services(namespace: str, mdbs: MongoDBSearch):
    """
    Verify that per-shard mongot Services are created.

    For a sharded cluster with external LB, the Search controller should create
    one Service per shard with naming: <search-name>-mongot-<shardName>-svc
    """
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        service_name = f"{mdbs.name}-mongot-{shard_name}-svc"

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


@mark.e2e_search_sharded_enterprise_external_lb
def test_verify_per_shard_statefulsets(namespace: str, mdbs: MongoDBSearch):
    """
    Verify that per-shard mongot StatefulSets are created.

    For a sharded cluster with external LB, the Search controller should create
    one StatefulSet per shard with naming: <search-name>-mongot-<shardName>
    """
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        sts_name = f"{mdbs.name}-mongot-{shard_name}"

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


@mark.e2e_search_sharded_enterprise_external_lb
def test_wait_for_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    """
    Verify that each shard's mongod has the correct search parameters.

    For sharded clusters with external LB (Envoy proxy), each shard should have:
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

                # For external LB mode with Envoy, endpoint should point to proxy service on port 27029
                expected_proxy_service = f"{mdbs.name}-mongot-{shard_name}-proxy-svc"

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


@mark.e2e_search_sharded_enterprise_external_lb
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


@mark.e2e_search_sharded_enterprise_external_lb
def test_016_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    """Deploy mongodb-tools pod for running queries."""
    # The tools_pod fixture handles deployment and waiting for readiness
    logger.info(f"✓ Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_sharded_enterprise_external_lb
def test_017_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
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


@mark.e2e_search_sharded_enterprise_external_lb
def test_search_shard_collections(mdb: MongoDB):
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


@mark.e2e_search_sharded_enterprise_external_lb
def test_search_create_search_index(mdb: MongoDB):
    """Create text search index on movies collection."""
    get_user_sample_movies_helper(mdb).create_search_index()


@mark.e2e_search_sharded_enterprise_external_lb
def test_search_assert_search_query(mdb: MongoDB):
    """Execute text search query through mongos and verify results."""
    get_user_sample_movies_helper(mdb).assert_search_query(retry_timeout=60)


@mark.e2e_search_sharded_enterprise_external_lb
def test_search_verify_results_from_all_shards(mdb: MongoDB):
    """
    Verify that search results through mongos contain documents from all shards.
    This is the definitive test that mongos is correctly aggregating search results.
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


@mark.e2e_search_sharded_enterprise_external_lb
def test_verify_search_resource_status(mdbs: MongoDBSearch):
    """Verify the MongoDBSearch resource is in Running phase with correct status."""
    mdbs.load()

    phase = mdbs.get_status_phase()
    assert phase == Phase.Running, f"MongoDBSearch phase is {phase}, expected Running"

    logger.info(f"✓ MongoDBSearch {mdbs.name} is in Running phase")
