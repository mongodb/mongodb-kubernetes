"""Deploy and manage a kube-prometheus-stack (Prometheus + Grafana + operator) in a k8s namespace."""
import os
import tempfile

import kubernetes.client
import yaml
from kubetester.kubetester import run_periodically
from kubetester.helm import helm_install_from_chart
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

PROMETHEUS_COMMUNITY_REPO = ("prometheus-community", "https://prometheus-community.github.io/helm-charts")
KUBE_PROMETHEUS_STACK_CHART = "prometheus-community/kube-prometheus-stack"
DEFAULT_RELEASE = "prom"
DEFAULT_VERSION = "61.9.0"


class PrometheusStack:
    """Deploy and interact with a kube-prometheus-stack Helm release.

    Usage::

        stack = PrometheusStack(namespace="monitoring")
        stack.deploy()
        stack.wait_for_ready()
        print(stack.grafana_url())
    """

    def __init__(
        self,
        namespace: str,
        release: str = DEFAULT_RELEASE,
        version: str = DEFAULT_VERSION,
        grafana_admin_password: str = "admin",
        extra_helm_values: dict = None,
    ):
        self.namespace = namespace
        self.release = release
        self.version = version
        self.grafana_admin_password = grafana_admin_password
        self._extra_helm_values = extra_helm_values or {}

    def deploy(self):
        """Install or upgrade the kube-prometheus-stack Helm chart."""
        values = self._build_values()
        with tempfile.NamedTemporaryFile(mode="w", suffix=".yaml", delete=False) as f:
            yaml.dump(values, f)
            override_path = f.name

        try:
            logger.info(f"Deploying kube-prometheus-stack release={self.release} namespace={self.namespace} version={self.version}")
            helm_install_from_chart(
                namespace=self.namespace,
                release=self.release,
                chart=KUBE_PROMETHEUS_STACK_CHART,
                version=self.version,
                custom_repo=PROMETHEUS_COMMUNITY_REPO,
                helm_args={},  # triggers --create-namespace in _create_helm_args
                override_path=override_path,
            )
            logger.info("kube-prometheus-stack deployed successfully")
        finally:
            os.unlink(override_path)

    def wait_for_ready(self, timeout: int = 300):
        """Wait until Prometheus and Grafana deployments are available."""
        apps_api = kubernetes.client.AppsV1Api()

        # Use stable app.kubernetes.io/name labels — independent of release name
        target_app_names = ["grafana", "kube-state-metrics", "kube-prometheus-stack-prometheus-operator"]

        def _deployments_ready() -> bool:
            try:
                deployments = apps_api.list_namespaced_deployment(self.namespace)
                found = {name: False for name in target_app_names}
                for dep in deployments.items:
                    app_name = dep.metadata.labels.get("app.kubernetes.io/name", "")
                    if app_name in found:
                        ready = dep.status.ready_replicas or 0
                        desired = dep.spec.replicas or 1
                        if ready >= desired:
                            found[app_name] = True
                not_ready = [n for n, ok in found.items() if not ok]
                if not_ready:
                    logger.debug(f"Waiting for deployments: {not_ready}")
                    return False
                return True
            except kubernetes.client.exceptions.ApiException as e:
                logger.debug(f"API error checking deployments: {e}")
                return False

        run_periodically(
            fn=_deployments_ready,
            timeout=timeout,
            sleep_time=10,
            msg="Prometheus stack deployments to be ready",
        )
        logger.info("Prometheus stack is ready")

    def prometheus_url(self, host: str = "localhost", node_port: int = None) -> str:
        """Return the Prometheus UI URL.

        By default returns the in-cluster service URL. Pass host/node_port for
        NodePort or port-forwarded access.
        """
        if node_port:
            return f"http://{host}:{node_port}"
        svc = f"{self.release}-kube-prometheus-stack-prometheus.{self.namespace}.svc.cluster.local"
        return f"http://{svc}:9090"

    def grafana_url(self, host: str = "localhost", node_port: int = None) -> str:
        """Return the Grafana UI URL.

        By default returns the in-cluster service URL. Pass host/node_port for
        NodePort or port-forwarded access.
        """
        if node_port:
            return f"http://{host}:{node_port}"
        svc = f"{self.release}-grafana.{self.namespace}.svc.cluster.local"
        return f"http://{svc}:80"

    def deploy_all(
        self,
        target_namespace: str,
        mdb_resource_name: str,
        scrape_interval: str = "30s",
        provision_dashboards: bool = True,
        wait_timeout: int = 300,
    ):
        """Deploy the full observability stack in one call.

        Steps:
          1. Deploy kube-prometheus-stack (Prometheus + Grafana + operator)
          2. Wait for all deployments to be ready
          3. Deploy ServiceMonitor for mongod (port 9216)
          4. Deploy ServiceMonitor for mongot (port 9946)
          5. Deploy PodMonitor for Envoy (port 9901 /stats/prometheus)
          6. Provision community Grafana dashboards via ConfigMaps (optional)

        Args:
            target_namespace: Namespace where the MongoDB/MongoDBSearch resources live.
            mdb_resource_name: Name of the MongoDB resource (drives label selectors).
            scrape_interval: Prometheus scrape interval for all monitors.
            provision_dashboards: If True, download and provision Envoy + MongoDB
                                  community dashboards via labeled ConfigMaps.
            wait_timeout: Seconds to wait for the stack to become ready.
        """
        from tests.common.prometheus.service_monitors import (
            deploy_mongod_service_monitor,
            deploy_mongot_service_monitor,
            deploy_envoy_service_monitor,
        )
        from tests.common.prometheus.dashboards import KNOWN_DASHBOARDS, provision_dashboard_configmap

        self.deploy()
        self.wait_for_ready(timeout=wait_timeout)

        deploy_mongod_service_monitor(
            monitoring_namespace=self.namespace,
            target_namespace=target_namespace,
            mdb_resource_name=mdb_resource_name,
            interval=scrape_interval,
        )
        deploy_mongot_service_monitor(
            monitoring_namespace=self.namespace,
            target_namespace=target_namespace,
            mdb_resource_name=mdb_resource_name,
            interval=scrape_interval,
        )
        deploy_envoy_service_monitor(
            monitoring_namespace=self.namespace,
            target_namespace=target_namespace,
            mdb_resource_name=mdb_resource_name,
            interval=scrape_interval,
        )

        if provision_dashboards:
            for label, dashboard_id in KNOWN_DASHBOARDS.items():
                if label == "mongodb-alt":
                    continue  # skip duplicate MongoDB dashboard
                provision_dashboard_configmap(
                    dashboard_id=dashboard_id,
                    configmap_name=f"grafana-dashboard-{label}",
                    namespace=self.namespace,
                )

        logger.info(
            f"Observability stack fully deployed — "
            f"Prometheus: {self.prometheus_url()} | Grafana: {self.grafana_url()}"
        )

    def _build_values(self) -> dict:
        values = {
            "grafana": {
                "adminPassword": self.grafana_admin_password,
                "sidecar": {
                    # Allow dashboards to be loaded from ConfigMaps in any namespace
                    "dashboards": {
                        "enabled": True,
                        "searchNamespace": "ALL",
                        "label": "grafana_dashboard",
                        "labelValue": "1",
                    }
                },
            },
            "prometheus": {
                "prometheusSpec": {
                    # Watch ServiceMonitors and PodMonitors in all namespaces
                    "serviceMonitorSelectorNilUsesHelmValues": False,
                    "podMonitorSelectorNilUsesHelmValues": False,
                    "serviceMonitorNamespaceSelector": {},
                    "podMonitorNamespaceSelector": {},
                    "serviceMonitorSelector": {},
                    "podMonitorSelector": {},
                }
            },
            # Disable components not needed for this use case
            "alertmanager": {"enabled": False},
            "kubeStateMetrics": {"enabled": True},
            "nodeExporter": {"enabled": False},
        }
        # Deep-merge extra values on top
        _deep_merge(values, self._extra_helm_values)
        return values


def _deep_merge(base: dict, override: dict) -> dict:
    for key, val in override.items():
        if key in base and isinstance(base[key], dict) and isinstance(val, dict):
            _deep_merge(base[key], val)
        else:
            base[key] = val
    return base
