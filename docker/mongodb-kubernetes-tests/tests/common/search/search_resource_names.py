"""Naming functions for MongoDBSearch Kubernetes resources.

These functions mirror the Go naming methods in api/v1/search/mongodbsearch_types.go
and must be kept in sync with them.

Go source: api/v1/search/mongodbsearch_types.go
"""

# ============================================================================
# Replica Set resources
# ============================================================================


def mongot_statefulset_name(search_name: str) -> str:
    """StatefulSet name for the (single/first) mongot instance.

    The operator always names RS mongot resources with a cluster index now
    (the legacy unindexed ``StatefulSetNamespacedName()`` branch was removed in
    favour of ``StatefulSetNamespacedNameForCluster(0)``), so the convenience
    root delegates to the cluster-0 indexed name.
    """
    return mongot_statefulset_name_for_cluster(search_name, 0)


def mongot_service_name(search_name: str) -> str:
    """Service name for the (single/first) mongot instance.

    Indexed to match the operator's ``SearchServiceNamespacedNameForCluster(0)``
    (the unindexed ``SearchServiceNamespacedName()`` branch was removed).
    """
    return mongot_service_name_for_cluster(search_name, 0)


def mongot_configmap_name(search_name: str) -> str:
    """ConfigMap name for the (single/first) mongot config.

    Indexed to match the operator's ``MongotConfigConfigMapNameForCluster(0)``
    (the unindexed ``MongotConfigConfigMapNamespacedName()`` is no longer used
    for RS resources).
    """
    return mongot_configmap_name_for_cluster(search_name, 0)


def search_state_configmap_name(search_name: str) -> str:
    """State ConfigMap name persisted by the search controllers.

    Matches the operator's ``searchcontroller.SearchStateCMName`` — deliberately
    ``<name>-search-state`` (not ``<name>-state``) so it cannot collide with the
    source MongoDB's StateStore ConfigMap when a MongoDBSearch shares its name.
    """
    return f"{search_name}-search-state"


def mongot_service_host(search_name: str, namespace: str, port: int) -> str:
    """Full hostname:port for the RS mongot Service."""
    return f"{mongot_service_name(search_name)}.{namespace}.svc.cluster.local:{port}"


def mongot_pod_fqdn(search_name: str, namespace: str, port: int) -> str:
    """Headless pod-0 FQDN for RS single mongot (pod-0.svc.ns.svc.cluster.local:port)."""
    return f"{mongot_statefulset_name(search_name)}-0.{mongot_service_name(search_name)}.{namespace}.svc.cluster.local:{port}"


def mongot_statefulset_name_for_cluster(search_name: str, cluster_index: int = 0) -> str:
    """Indexed mongot StatefulSet name. Mirrors StatefulSetNamespacedNameForCluster()."""
    return f"{search_name}-search-{cluster_index}"


def mongot_service_name_for_cluster(search_name: str, cluster_index: int = 0) -> str:
    """Indexed mongot headless Service name. Mirrors SearchServiceNamespacedNameForCluster()."""
    return f"{search_name}-search-{cluster_index}-svc"


def mongot_configmap_name_for_cluster(search_name: str, cluster_index: int = 0) -> str:
    """Indexed mongot ConfigMap name. Mirrors MongotConfigConfigMapNameForCluster()."""
    return f"{search_name}-search-{cluster_index}-config"


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


def operator_managed_tls_secret_name(search_name: str) -> str:
    """Operator-created copy of the RS mongot TLS certificate Secret."""
    return f"{search_name}-search-certificate-key"


# ============================================================================
# Sharded cluster resources
# ============================================================================


def shard_statefulset_name(search_name: str, shard_name: str, cluster_index: int = 0) -> str:
    """Per-shard mongot StatefulSet name. Mirrors MongotStatefulSetForClusterShard()."""
    return f"{search_name}-search-{cluster_index}-{shard_name}"


def shard_service_name(search_name: str, shard_name: str, cluster_index: int = 0) -> str:
    """Per-shard mongot Service name. Mirrors MongotServiceForClusterShard()."""
    return f"{search_name}-search-{cluster_index}-{shard_name}-svc"


def shard_configmap_name(search_name: str, shard_name: str, cluster_index: int = 0) -> str:
    """Per-shard mongot ConfigMap name. Mirrors MongotConfigMapForClusterShard()."""
    return f"{search_name}-search-{cluster_index}-{shard_name}-config"


def shard_tls_cert_name(
    search_name: str, shard_name: str, certs_secret_prefix: str = "", cluster_index: int = 0
) -> str:
    """Per-shard TLS certificate secret name. Mirrors TLSSecretForClusterShard().

    Pattern:
      - With prefix: {certs_secret_prefix}-{search_name}-search-{cluster_index}-{shard_name}-cert
      - Without prefix: {search_name}-search-{cluster_index}-{shard_name}-cert
    """
    if certs_secret_prefix:
        return f"{certs_secret_prefix}-{search_name}-search-{cluster_index}-{shard_name}-cert"
    return f"{search_name}-search-{cluster_index}-{shard_name}-cert"


def shard_operator_managed_tls_secret_name(search_name: str, shard_name: str, cluster_index: int = 0) -> str:
    """Operator-created copy of a shard mongot TLS certificate Secret."""
    return f"{search_name}-search-{cluster_index}-{shard_name}-certificate-key"


def shard_proxy_service_name(search_name: str, shard_name: str, cluster_index: int = 0) -> str:
    """Per-shard stable proxy Service name. Mirrors ProxyServiceNameForClusterShard()."""
    return f"{search_name}-search-{cluster_index}-{shard_name}-proxy-svc"


def shard_service_host(search_name: str, shard_name: str, namespace: str, port: int, cluster_index: int = 0) -> str:
    """Full hostname:port for the per-shard mongot Service."""
    return f"{shard_service_name(search_name, shard_name, cluster_index)}.{namespace}.svc.cluster.local:{port}"


def shard_pod_fqdn(search_name: str, shard_name: str, namespace: str, port: int, cluster_index: int = 0) -> str:
    """Headless pod-0 FQDN for per-shard single mongot (pod-0.svc.ns.svc.cluster.local:port)."""
    return f"{shard_statefulset_name(search_name, shard_name, cluster_index)}-0.{shard_service_name(search_name, shard_name, cluster_index)}.{namespace}.svc.cluster.local:{port}"


def shard_proxy_service_host(
    search_name: str, shard_name: str, namespace: str, port: int, cluster_index: int = 0
) -> str:
    """Full hostname:port for the per-shard proxy Service."""
    return f"{shard_proxy_service_name(search_name, shard_name, cluster_index)}.{namespace}.svc.cluster.local:{port}"


# ============================================================================
# Multi-cluster resources
# ============================================================================


def mc_proxy_svc_name(search_name: str, cluster_index: int) -> str:
    """Per-cluster proxy Service name. Mirrors ProxyServiceNamespacedNameForCluster().

    Serves two roles with the same name: ``mongotHost`` for RS-MC mongod, and the
    shard-agnostic mongos target for sharded-MC (cluster-level routing).
    """
    return f"{search_name}-search-{cluster_index}-proxy-svc"


def mc_proxy_svc_fqdn(search_name: str, namespace: str, cluster_index: int) -> str:
    """Fully-qualified mc_proxy_svc hostname (no port; callers append ``:<port>``)."""
    return f"{mc_proxy_svc_name(search_name, cluster_index)}.{namespace}.svc.cluster.local"


def shard_proxy_svc_hostname_template(search_name: str, namespace: str, cluster_index: int = 0) -> str:
    """externalHostname for a sharded managed LB: the per-shard proxy-svc FQDN
    with the {shardName} placeholder kept for the operator to resolve."""
    return f"{search_name}-search-{cluster_index}-{{shardName}}-proxy-svc.{namespace}.svc.cluster.local"


def mc_search_artifact_names(search_name: str, cluster_index: int) -> dict[str, str]:
    """Names of the operator-managed per-cluster Search artifacts (RS-MC managed-LB set)."""
    return {
        "sts": mongot_statefulset_name_for_cluster(search_name, cluster_index),
        "svc": mongot_service_name_for_cluster(search_name, cluster_index),
        "proxy": mc_proxy_svc_name(search_name, cluster_index),
        "mongot_cm": mongot_configmap_name_for_cluster(search_name, cluster_index),
        "envoy_deployment": lb_deployment_name(search_name, cluster_index),
        "envoy_cm": lb_configmap_name(search_name, cluster_index),
        "operator_tls_secret": operator_managed_tls_secret_name(search_name),
    }


# ============================================================================
# Managed load balancer resources
# ============================================================================


def lb_deployment_name(search_name: str, cluster_index: int = 0) -> str:
    """Managed LB Deployment name. Mirrors LoadBalancerDeploymentNameForCluster().

    cluster_index defaults to 0 for single-cluster callers; pass the cluster
    position explicitly for multi-cluster tests.
    """
    return f"{search_name}-search-lb-{cluster_index}"


def lb_configmap_name(search_name: str, cluster_index: int = 0) -> str:
    """Managed LB ConfigMap name. Mirrors LoadBalancerConfigMapNameForCluster().

    cluster_index defaults to 0 for single-cluster callers; pass the cluster
    position explicitly for multi-cluster tests.
    """
    return f"{search_name}-search-lb-{cluster_index}-config"


def lb_server_cert_name(search_name: str, certs_secret_prefix: str = "", cluster_index: int = 0) -> str:
    """Managed LB server TLS certificate secret name. Mirrors LoadBalancerServerCert().

    The cluster index matches the per-cluster Envoy Deployment name. Defaults to 0
    for single-cluster callers; pass the cluster position for multi-cluster tests.

    Pattern:
      - With prefix: {certs_secret_prefix}-{search_name}-search-lb-{cluster_index}-cert
      - Without prefix: {search_name}-search-lb-{cluster_index}-cert
    """
    if certs_secret_prefix:
        return f"{certs_secret_prefix}-{search_name}-search-lb-{cluster_index}-cert"
    return f"{search_name}-search-lb-{cluster_index}-cert"


def lb_client_cert_name(search_name: str, certs_secret_prefix: str = "", cluster_index: int = 0) -> str:
    """Managed LB client TLS certificate secret name. Mirrors LoadBalancerClientCert().

    The cluster index matches the per-cluster Envoy Deployment name. Defaults to 0
    for single-cluster callers; pass the cluster position for multi-cluster tests.

    Pattern:
      - With prefix: {certs_secret_prefix}-{search_name}-search-lb-{cluster_index}-client-cert
      - Without prefix: {search_name}-search-lb-{cluster_index}-client-cert
    """
    if certs_secret_prefix:
        return f"{certs_secret_prefix}-{search_name}-search-lb-{cluster_index}-client-cert"
    return f"{search_name}-search-lb-{cluster_index}-client-cert"


# ============================================================================
# Metrics forwarder resources
# ============================================================================


def metrics_forwarder_deployment_name(search_name: str, cluster_index: int = 0) -> str:
    """Deployment name for the metrics forwarder. Mirrors MetricsForwarderDeploymentNameForCluster().

    cluster_index defaults to 0 for single-cluster callers; pass the cluster
    position explicitly for multi-cluster tests.
    """
    return f"{search_name}-search-metrics-forwarder-{cluster_index}"


def metrics_forwarder_configmap_name(search_name: str, cluster_index: int = 0) -> str:
    """ConfigMap name for the metrics forwarder config. Mirrors MetricsForwarderConfigMapNameForCluster().

    cluster_index defaults to 0 for single-cluster callers; pass the cluster
    position explicitly for multi-cluster tests.
    """
    return f"{search_name}-search-metrics-forwarder-{cluster_index}-config"
