"""Create Prometheus ServiceMonitor and PodMonitor CRDs for MongoDB components.

Targets:
  - mongod:  port 9216 (spec.prometheus on MongoDB CR enables this exporter)
  - mongot:  port 9946 (spec.prometheus on MongoDBSearch CR)
  - Envoy:   port 9901 /stats/prometheus (admin interface exposed by the Envoy sidecar)
"""
from typing import Optional

import kubernetes.client
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

_MONITORING_GROUP = "monitoring.coreos.com"
_MONITORING_VERSION = "v1"


def deploy_service_monitor(
    name: str,
    namespace: str,
    match_labels: dict,
    port: str,
    path: str = "/metrics",
    interval: str = "30s",
    scrape_timeout: str = "10s",
    target_namespace: Optional[str] = None,
    scheme: str = "http",
    tls_config: Optional[dict] = None,
):
    """Create or replace a ServiceMonitor CRD.

    Args:
        name: Name of the ServiceMonitor resource.
        namespace: Namespace to create the ServiceMonitor in (usually the monitoring namespace).
        match_labels: Label selector for Services to scrape.
        port: Named port on the Service to scrape.
        path: HTTP path to scrape. Defaults to /metrics.
        interval: Scrape interval (e.g. "30s").
        scrape_timeout: Scrape timeout (e.g. "10s").
        target_namespace: Restrict scraping to this namespace. If None, scrapes all namespaces.
        scheme: http or https.
        tls_config: Optional TLS configuration dict for the ServiceMonitor spec.
    """
    endpoint = {
        "port": port,
        "path": path,
        "interval": interval,
        "scrapeTimeout": scrape_timeout,
        "scheme": scheme,
    }
    if tls_config:
        endpoint["tlsConfig"] = tls_config

    ns_selector = {"matchNames": [target_namespace]} if target_namespace else {}

    body = {
        "apiVersion": f"{_MONITORING_GROUP}/{_MONITORING_VERSION}",
        "kind": "ServiceMonitor",
        "metadata": {
            "name": name,
            "namespace": namespace,
        },
        "spec": {
            "selector": {"matchLabels": match_labels},
            "namespaceSelector": ns_selector,
            "endpoints": [endpoint],
        },
    }

    _apply_custom_object(
        group=_MONITORING_GROUP,
        version=_MONITORING_VERSION,
        namespace=namespace,
        plural="servicemonitors",
        name=name,
        body=body,
    )
    logger.info(f"ServiceMonitor '{name}' applied in namespace '{namespace}'")


def deploy_pod_monitor(
    name: str,
    namespace: str,
    match_labels: dict,
    port: str,
    path: str = "/metrics",
    interval: str = "30s",
    scrape_timeout: str = "10s",
    target_namespace: Optional[str] = None,
):
    """Create or replace a PodMonitor CRD.

    Args:
        name: Name of the PodMonitor resource.
        namespace: Namespace to create the PodMonitor in.
        match_labels: Label selector for Pods to scrape.
        port: Named port on the Pod container to scrape.
        path: HTTP path to scrape. Defaults to /metrics.
        interval: Scrape interval.
        scrape_timeout: Scrape timeout.
        target_namespace: Restrict scraping to this namespace. If None, scrapes all namespaces.
    """
    ns_selector = {"matchNames": [target_namespace]} if target_namespace else {}

    body = {
        "apiVersion": f"{_MONITORING_GROUP}/{_MONITORING_VERSION}",
        "kind": "PodMonitor",
        "metadata": {
            "name": name,
            "namespace": namespace,
        },
        "spec": {
            "selector": {"matchLabels": match_labels},
            "namespaceSelector": ns_selector,
            "podMetricsEndpoints": [
                {
                    "port": port,
                    "path": path,
                    "interval": interval,
                    "scrapeTimeout": scrape_timeout,
                }
            ],
        },
    }

    _apply_custom_object(
        group=_MONITORING_GROUP,
        version=_MONITORING_VERSION,
        namespace=namespace,
        plural="podmonitors",
        name=name,
        body=body,
    )
    logger.info(f"PodMonitor '{name}' applied in namespace '{namespace}'")


def deploy_mongod_service_monitor(
    monitoring_namespace: str,
    target_namespace: str,
    mdb_resource_name: str,
    interval: str = "30s",
):
    """Deploy a ServiceMonitor to scrape mongod Prometheus metrics (port 9216).

    Requires spec.prometheus to be enabled on the MongoDB CR, which causes the
    operator to expose a Service with port 9216 on the mongod pods.

    Args:
        monitoring_namespace: Namespace where the ServiceMonitor will be created.
        target_namespace: Namespace where the MongoDB CR and its Service live.
        mdb_resource_name: Name of the MongoDB resource (used to build label selectors).
        interval: Scrape interval.
    """
    deploy_service_monitor(
        name=f"{mdb_resource_name}-mongod",
        namespace=monitoring_namespace,
        match_labels={"app": mdb_resource_name},
        port="prometheus",
        path="/metrics",
        interval=interval,
        target_namespace=target_namespace,
    )


def deploy_mongot_service_monitor(
    monitoring_namespace: str,
    target_namespace: str,
    mdb_resource_name: str,
    interval: str = "30s",
):
    """Deploy a ServiceMonitor to scrape mongot Prometheus metrics (port 9946).

    Requires spec.prometheus to be enabled on the MongoDBSearch CR, which causes the
    operator to expose a Service port named "metrics" on the mongot pods.

    Args:
        monitoring_namespace: Namespace where the ServiceMonitor will be created.
        target_namespace: Namespace where the MongoDBSearch CR and its Service live.
        mdb_resource_name: Name of the MongoDB resource (used to build label selectors).
        interval: Scrape interval.
    """
    deploy_service_monitor(
        name=f"{mdb_resource_name}-mongot",
        namespace=monitoring_namespace,
        match_labels={"app": mdb_resource_name},
        port="metrics",
        path="/metrics",
        interval=interval,
        target_namespace=target_namespace,
    )


def deploy_envoy_service_monitor(
    monitoring_namespace: str,
    target_namespace: str,
    mdb_resource_name: str,
    interval: str = "30s",
):
    """Deploy a ServiceMonitor to scrape Envoy metrics (port 9901 /stats/prometheus).

    Requires the operator to expose a Service port named "metrics" on the Envoy proxy
    pod (label app={mdb_resource_name}-search-lb). The operator should expose port 9901
    under the name "metrics" rather than "admin" to follow the standard convention.

    Args:
        monitoring_namespace: Namespace where the ServiceMonitor will be created.
        target_namespace: Namespace where the Envoy pod and its Service live.
        mdb_resource_name: Name of the MongoDB resource.
        interval: Scrape interval.
    """
    deploy_service_monitor(
        name=f"{mdb_resource_name}-envoy",
        namespace=monitoring_namespace,
        match_labels={"app": f"{mdb_resource_name}-search-lb"},
        port="metrics",
        path="/stats/prometheus",
        interval=interval,
        target_namespace=target_namespace,
    )


def _apply_custom_object(group: str, version: str, namespace: str, plural: str, name: str, body: dict):
    """Create or patch a custom object (idempotent)."""
    api = kubernetes.client.CustomObjectsApi()
    try:
        api.create_namespaced_custom_object(
            group=group,
            version=version,
            namespace=namespace,
            plural=plural,
            body=body,
        )
        logger.debug(f"Created {plural}/{name} in {namespace}")
    except kubernetes.client.exceptions.ApiException as e:
        if e.status == 409:
            # Already exists — patch it (no resourceVersion required for strategic merge patch)
            api.patch_namespaced_custom_object(
                group=group,
                version=version,
                namespace=namespace,
                plural=plural,
                name=name,
                body=body,
            )
            logger.debug(f"Patched existing {plural}/{name} in {namespace}")
        else:
            raise
