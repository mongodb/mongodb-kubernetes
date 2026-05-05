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


def mongot_pod_fqdn(search_name: str, namespace: str, port: int) -> str:
    """Headless pod-0 FQDN for RS single mongot (pod-0.svc.ns.svc.cluster.local:port)."""
    return f"{mongot_statefulset_name(search_name)}-0.{mongot_service_name(search_name)}.{namespace}.svc.cluster.local:{port}"


def proxy_service_name(search_name: str) -> str:
    """Stable proxy Service name for RS. Mirrors ProxyServiceNamespacedName()."""
    return f"{search_name}-search-0-proxy-svc"


def proxy_service_host(search_name: str, namespace: str, port: int) -> str:
    """Full hostname:port for the RS proxy Service."""
    return f"{proxy_service_name(search_name)}.{namespace}.svc.cluster.local:{port}"


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
    """Per-shard stable proxy Service name. Mirrors ProxyServiceNameForShard()."""
    return f"{search_name}-search-0-{shard_name}-proxy-svc"


def shard_service_host(search_name: str, shard_name: str, namespace: str, port: int) -> str:
    """Full hostname:port for the per-shard mongot Service."""
    return f"{shard_service_name(search_name, shard_name)}.{namespace}.svc.cluster.local:{port}"


def shard_pod_fqdn(search_name: str, shard_name: str, namespace: str, port: int) -> str:
    """Headless pod-0 FQDN for per-shard single mongot (pod-0.svc.ns.svc.cluster.local:port)."""
    return f"{shard_statefulset_name(search_name, shard_name)}-0.{shard_service_name(search_name, shard_name)}.{namespace}.svc.cluster.local:{port}"


def shard_proxy_service_host(search_name: str, shard_name: str, namespace: str, port: int) -> str:
    """Full hostname:port for the per-shard proxy Service."""
    return f"{shard_proxy_service_name(search_name, shard_name)}.{namespace}.svc.cluster.local:{port}"


# ============================================================================
# Managed load balancer resources
# ============================================================================


def lb_deployment_name(search_name: str, cluster_index: int = 0) -> str:
    """Managed LB Deployment name. Mirrors LoadBalancerDeploymentNameForCluster().

    cluster_index defaults to 0 for single-cluster callers; pass the cluster
    position explicitly for multi-cluster tests.
    """
    return f"{search_name}-search-lb-0-{cluster_index}"


def lb_configmap_name(search_name: str, cluster_index: int = 0) -> str:
    """Managed LB ConfigMap name. Mirrors LoadBalancerConfigMapNameForCluster().

    cluster_index defaults to 0 for single-cluster callers; pass the cluster
    position explicitly for multi-cluster tests.
    """
    return f"{search_name}-search-lb-0-{cluster_index}-config"


def lb_server_cert_name(search_name: str, certs_secret_prefix: str = "") -> str:
    """Managed LB server TLS certificate secret name. Mirrors LoadBalancerServerCert().

    Pattern:
      - With prefix: {certs_secret_prefix}-{search_name}-search-lb-0-cert
      - Without prefix: {search_name}-search-lb-0-cert
    """
    if certs_secret_prefix:
        return f"{certs_secret_prefix}-{search_name}-search-lb-0-cert"
    return f"{search_name}-search-lb-0-cert"


def lb_client_cert_name(search_name: str, certs_secret_prefix: str = "") -> str:
    """Managed LB client TLS certificate secret name. Mirrors LoadBalancerClientCert().

    Pattern:
      - With prefix: {certs_secret_prefix}-{search_name}-search-lb-0-client-cert
      - Without prefix: {search_name}-search-lb-0-client-cert
    """
    if certs_secret_prefix:
        return f"{certs_secret_prefix}-{search_name}-search-lb-0-client-cert"
    return f"{search_name}-search-lb-0-client-cert"
