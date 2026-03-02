from tests import test_logger
from tests.common.search import search_resource_names
from kubetester.certs import create_tls_certs
from kubetester import create_or_update_configmap, create_or_update_secret, try_load
from kubetester.certs import create_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from tests.common.search import search_resource_names
from tests.common.search.search_tester import SearchTester
from kubernetes import client
from kubetester.kubetester import KubernetesTester, run_periodically
from tests.conftest import get_issuer_ca_filepath

logger = test_logger.get_test_logger(__name__)


def create_per_shard_search_tls_certs(
        namespace: str, 
        issuer: str, 
        prefix: str, 
        shard_count: int, 
        mdb_resource_name: str, 
        mdbs_resource_name: str,
):
    """
        Create per-shard TLS certificates for MongoDBSearch resource.

        For each shard, creates a certificate with DNS names for:
        - The mongot service: {search-name}-search-0-{shardName}-svc.{namespace}.svc.cluster.local
        - The proxy service: {search-name}-search-0-{shardName}-proxy-svc.{namespace}.svc.cluster.local

    a    Secret naming: search_resource_names.shard_tls_cert_name(MDB_RESOURCE_NAME, shardName, prefix)
        e.g., certs-mdb-sh-search-0-mdb-sh-0-cert
    """
    logger.info(f"Creating per-shard Search TLS certificates with prefix '{prefix}'...")

    for shard_idx in range(shard_count):
        shard_name = f"{mdb_resource_name}-{shard_idx}"
        secret_name = search_resource_names.shard_tls_cert_name(mdbs_resource_name, shard_name, prefix)

        additional_domains = [
            f"{search_resource_names.shard_service_name(mdbs_resource_name, shard_name)}.{namespace}.svc.cluster.local",
            f"{search_resource_names.shard_proxy_service_name(mdbs_resource_name, shard_name)}.{namespace}.svc.cluster.local",
        ]

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=search_resource_names.shard_statefulset_name(mdbs_resource_name, shard_name),
            secret_name=secret_name,
            additional_domains=additional_domains,
        )
        logger.info(f"✓ Per-shard Search TLS certificate created: {secret_name}")

    logger.info(f"✓ All {shard_count} per-shard Search TLS certificates created")


def make_admin_user(
        namespace: str,
        mdb_resource_name: str,
        admin_user_name: str,
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-mdb-admin.yaml"),
        namespace=namespace,
        name=admin_user_name,
    )

    if try_load(resource):
        return resource

    resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"

    return resource

def get_search_tester(mdb: MongoDB, username: str, password: str, use_ssl: bool = False) -> SearchTester:
    """Replaces both get_admin_search_tester and get_user_search_tester.
    Callers just pass the appropriate credentials."""
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_sharded(mdb, username, password, use_ssl=use_ssl, ca_path=ca_path)

def make_user(namespace: str, mdb_resource_name: str, user_name: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-mdb-user.yaml"),
        namespace=namespace,
        name=user_name,
    )
    if try_load(resource):
        return resource
    resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
    resource["spec"]["username"] = resource.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
    return resource

def make_mongot_user(
    namespace: str, mdbs: MongoDBSearch, mdb_resource_name: str, mongot_user_name: str
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
        namespace=namespace,
        name=f"{mdbs.name}-{mongot_user_name}",
    )
    if try_load(resource):
        return resource
    resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
    resource["spec"]["username"] = mongot_user_name
    resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
    return resource

def create_sharded_ca(issuer_ca_filepath: str, namespace: str, ca_configmap_name: str) -> str:
    ca = open(issuer_ca_filepath).read()
    configmap_data = {"ca-pem": ca, "mms-ca.crt": ca}
    create_or_update_configmap(namespace, ca_configmap_name, configmap_data)
    secret_data = {"ca.crt": ca}
    create_or_update_secret(namespace, ca_configmap_name, secret_data)
    return ca_configmap_name

def create_envoy_deployment(namespace: str, ca_configmap_name: str, envoy_proxy_port: int, envoy_admin_port: int):
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
                                {"name": "grpc", "containerPort": envoy_proxy_port},
                                {"name": "admin", "containerPort": envoy_admin_port},
                            ],
                            "resources": {
                                "requests": {"cpu": "100m", "memory": "128Mi"},
                                "limits": {"cpu": "500m", "memory": "512Mi"},
                            },
                            "readinessProbe": {
                                "httpGet": {"path": "/ready", "port": envoy_admin_port},
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
                            "configMap": {"name": ca_configmap_name, "items": [{"key": "ca-pem", "path": "ca-pem"}]},
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

def wait_for_envoy_ready(namespace: str, timeout: int = 120):
    def check_envoy_ready():
        try:
            apps_v1 = client.AppsV1Api()
            deployment = apps_v1.read_namespaced_deployment("envoy-proxy", namespace)
            ready = deployment.status.ready_replicas or 0
            return ready >= 1, f"Envoy ready replicas: {ready}"
        except Exception as e:
            return False, f"Error checking Envoy: {e}"

    run_periodically(check_envoy_ready, timeout=timeout, sleep_time=5, msg="Envoy proxy to be ready")

def create_envoy_proxy_services(
    namespace: str,
    mdbs_resource_name: str,
    mdb_resource_name: str,
    shard_count: int,
    envoy_proxy_port: int,
):
    """Create per-shard proxy Services pointing to Envoy."""
    for i in range(shard_count):
        shard_name = f"{mdb_resource_name}-{i}"
        proxy_svc_name = search_resource_names.shard_proxy_service_name(mdbs_resource_name, shard_name)

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
                "ports": [{"name": "grpc", "port": envoy_proxy_port, "targetPort": envoy_proxy_port}],
            },
        }

        try:
            KubernetesTester.create_service(namespace, service)
            logger.info(f"✓ Proxy Service {proxy_svc_name} created")
        except Exception as e:
            logger.info(f"Proxy Service {proxy_svc_name} may already exist: {e}")

def create_envoy_certificates(
    namespace: str,
    issuer: str,
    mdbs_resource_name: str,
    mdb_resource_name: str,
    shard_count: int,
):
    """Create TLS certificates for Envoy proxy."""
    logger.info("Creating Envoy proxy certificates...")

    # Build SANs for server certificate (all per-shard proxy services)
    additional_domains = []
    for i in range(shard_count):
        shard_name = f"{mdb_resource_name}-{i}"
        proxy_svc = search_resource_names.shard_proxy_service_name(mdbs_resource_name, shard_name)
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