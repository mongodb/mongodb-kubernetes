"""Naming functions for MongoDBSearch Kubernetes resources.

These functions mirror the Go naming methods in api/v1/search/mongodbsearch_types.go
and must be kept in sync with them.

Go source: api/v1/search/mongodbsearch_types.go
"""

# ============================================================================
# Replica Set resources
# ============================================================================


def mongot_statefulset_name(search_name: str) -> str:
    """StatefulSet name for the mongot instance. Mirrors StatefulSetNamespacedName()."""
    return f"{search_name}-search"


def mongot_service_name(search_name: str) -> str:
    """Service name for the mongot instance. Mirrors SearchServiceNamespacedName()."""
    return f"{search_name}-search-svc"


def mongot_configmap_name(search_name: str) -> str:
    """ConfigMap name for the mongot config. Mirrors MongotConfigConfigMapNamespacedName()."""
    return f"{search_name}-search-config"


def mongot_service_host(search_name: str, namespace: str, port: int) -> str:
    """Full hostname:port for the RS mongot Service."""
    return f"{mongot_service_name(search_name)}.{namespace}.svc.cluster.local:{port}"


def entrypoint_service_name(search_name: str) -> str:
    """Entrypoint Service name. Mirrors EntrypointServiceNamespacedName()."""
    return f"{search_name}-search-ep-svc"


def entrypoint_service_host(search_name: str, namespace: str, port: int) -> str:
    """Full hostname:port for the RS entrypoint Service."""
    return f"{entrypoint_service_name(search_name)}.{namespace}.svc.cluster.local:{port}"


def mongot_tls_cert_name(search_name: str, certs_secret_prefix: str = "") -> str:
    """TLS certificate secret name for RS mongot. Mirrors TLSSecretNamespacedName().

    Pattern:
      - With prefix: {certs_secret_prefix}-{search_name}-search-cert
      - Without prefix: {search_name}-search-cert
    """
    if certs_secret_prefix:
        return f"{certs_secret_prefix}-{search_name}-search-cert"
    return f"{search_name}-search-cert"


# ============================================================================
# Sharded cluster resources
# ============================================================================


def shard_statefulset_name(search_name: str, shard_name: str) -> str:
    """Per-shard mongot StatefulSet name. Mirrors MongotStatefulSetForShard()."""
    return f"{search_name}-search-0-{shard_name}"


def shard_service_name(search_name: str, shard_name: str) -> str:
    """Per-shard mongot Service name. Mirrors MongotServiceForShard()."""
    return f"{search_name}-search-0-{shard_name}-svc"


def shard_configmap_name(search_name: str, shard_name: str) -> str:
    """Per-shard mongot ConfigMap name. Mirrors MongotConfigMapForShard()."""
    return f"{search_name}-search-0-{shard_name}-config"


def shard_tls_cert_name(search_name: str, shard_name: str, certs_secret_prefix: str = "") -> str:
    """Per-shard TLS certificate secret name. Mirrors TLSSecretForShard().

    Pattern:
      - With prefix: {certs_secret_prefix}-{search_name}-search-0-{shard_name}-cert
      - Without prefix: {search_name}-search-0-{shard_name}-cert
    """
    if certs_secret_prefix:
        return f"{certs_secret_prefix}-{search_name}-search-0-{shard_name}-cert"
    return f"{search_name}-search-0-{shard_name}-cert"


def shard_proxy_service_name(search_name: str, shard_name: str) -> str:
    """Per-shard SNI proxy Service name. Mirrors LoadBalancerProxyServiceNameForShard()."""
    return f"{search_name}-search-0-{shard_name}-proxy-svc"


def shard_service_host(search_name: str, shard_name: str, namespace: str, port: int) -> str:
    """Full hostname:port for the per-shard mongot Service."""
    return f"{shard_service_name(search_name, shard_name)}.{namespace}.svc.cluster.local:{port}"


def shard_proxy_service_host(search_name: str, shard_name: str, namespace: str, port: int) -> str:
    """Full hostname:port for the per-shard proxy Service."""
    return f"{shard_proxy_service_name(search_name, shard_name)}.{namespace}.svc.cluster.local:{port}"


# ============================================================================
# Managed load balancer resources
# ============================================================================


def lb_deployment_name(search_name: str) -> str:
    """Managed LB Deployment name. Mirrors LoadBalancerDeploymentName()."""
    return f"{search_name}-search-lb"


def lb_configmap_name(search_name: str) -> str:
    """Managed LB ConfigMap name. Mirrors LoadBalancerConfigMapName()."""
    return f"{search_name}-search-lb-config"


def lb_service_name(search_name: str) -> str:
    """Managed LB ClusterIP Service name. Mirrors LoadBalancerServiceName()."""
    return f"{search_name}-search-lb-svc"


def lb_proxy_service_host(search_name: str, namespace: str, port: int = 27029) -> str:
    """Full hostname:port for the RS managed LB proxy Service."""
    return f"{lb_service_name(search_name)}.{namespace}.svc.cluster.local:{port}"


def lb_server_cert_name(search_name: str, certs_secret_prefix: str = "") -> str:
    """Managed LB server TLS certificate secret name. Mirrors LoadBalancerServerCert().

    Pattern:
      - With prefix: {certs_secret_prefix}-{search_name}-search-lb-cert
      - Without prefix: {search_name}-search-lb-cert
    """
    if certs_secret_prefix:
        return f"{certs_secret_prefix}-{search_name}-search-lb-cert"
    return f"{search_name}-search-lb-cert"


def lb_client_cert_name(search_name: str, certs_secret_prefix: str = "") -> str:
    """Managed LB client TLS certificate secret name. Mirrors LoadBalancerClientCert().

    Pattern:
      - With prefix: {certs_secret_prefix}-{search_name}-search-lb-client-cert
      - Without prefix: {search_name}-search-lb-client-cert
    """
    if certs_secret_prefix:
        return f"{certs_secret_prefix}-{search_name}-search-lb-client-cert"
    return f"{search_name}-search-lb-client-cert"
