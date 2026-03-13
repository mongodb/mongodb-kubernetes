"""Grafana dashboard utilities for MongoDB monitoring.

Provides constants for known community dashboards and helpers to import them
into a running Grafana instance — either via the Grafana HTTP API or by
creating a ConfigMap that the Grafana sidecar picks up automatically.
"""
import json
import urllib.error
import urllib.parse
import urllib.request

import kubernetes.client
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

# ---------------------------------------------------------------------------
# Known community dashboard IDs from grafana.com
# ---------------------------------------------------------------------------
# Envoy proxy dashboard:  https://grafana.com/grafana/dashboards/11022
# MongoDB overview:       https://grafana.com/grafana/dashboards/12079
# MongoDB Atlas (alt):    https://grafana.com/grafana/dashboards/14763
#
# Note: There is no publicly available official Grafana dashboard for mongot.
# mongot metrics (port 9946) can be explored with auto-generated dashboards
# in Grafana Explore or by building a custom dashboard.

KNOWN_DASHBOARDS: dict[str, int] = {
    "envoy": 11022,
    "mongodb": 12079,
    "mongodb-alt": 14763,
}


def import_community_dashboard(
    dashboard_id: int,
    grafana_url: str,
    grafana_user: str = "admin",
    grafana_password: str = "admin",
    datasource_name: str = "Prometheus",
    folder_id: int = 0,
) -> dict:
    """Download a community dashboard from grafana.com and import it into Grafana.

    Downloads the dashboard JSON from the Grafana.com API and POSTs it to the
    Grafana instance's dashboard import endpoint.

    Args:
        dashboard_id: Grafana.com dashboard ID (e.g. 11022 for Envoy).
        grafana_url: Base URL of the Grafana instance, e.g. "http://localhost:3000".
        grafana_user: Grafana admin username.
        grafana_password: Grafana admin password.
        datasource_name: Name of the Prometheus datasource in Grafana to use as
                         the default datasource in the imported dashboard.
        folder_id: Grafana folder ID to place the dashboard in. 0 = General.

    Returns:
        The response JSON from the Grafana import API.

    Raises:
        urllib.error.URLError: If the download or import request fails.
        RuntimeError: If the Grafana API returns a non-success response.
    """
    dashboard_json = _download_dashboard_from_grafana_com(dashboard_id)
    return _import_dashboard(
        dashboard_json=dashboard_json,
        grafana_url=grafana_url,
        grafana_user=grafana_user,
        grafana_password=grafana_password,
        datasource_name=datasource_name,
        folder_id=folder_id,
    )


def provision_dashboard_configmap(
    dashboard_id: int,
    configmap_name: str,
    namespace: str,
    grafana_label: str = "grafana_dashboard",
    grafana_label_value: str = "1",
) -> str:
    """Download a community dashboard and provision it via a Grafana sidecar ConfigMap.

    The Grafana sidecar watches for ConfigMaps with a specific label and
    automatically loads any JSON files found as dashboards. This approach does
    not require direct Grafana API access and works well in CI environments.

    Args:
        dashboard_id: Grafana.com dashboard ID.
        configmap_name: Name for the ConfigMap to create/replace.
        namespace: Kubernetes namespace for the ConfigMap.
        grafana_label: Label key that the Grafana sidecar watches.
        grafana_label_value: Label value to apply.

    Returns:
        The name of the created ConfigMap.
    """
    dashboard_json = _download_dashboard_from_grafana_com(dashboard_id)
    json_str = json.dumps(dashboard_json, indent=2)

    core_api = kubernetes.client.CoreV1Api()
    body = kubernetes.client.V1ConfigMap(
        metadata=kubernetes.client.V1ObjectMeta(
            name=configmap_name,
            namespace=namespace,
            labels={grafana_label: grafana_label_value},
        ),
        data={f"dashboard-{dashboard_id}.json": json_str},
    )

    try:
        core_api.create_namespaced_config_map(namespace=namespace, body=body)
        logger.info(f"Created ConfigMap '{configmap_name}' for dashboard {dashboard_id} in '{namespace}'")
    except kubernetes.client.exceptions.ApiException as e:
        if e.status == 409:
            core_api.replace_namespaced_config_map(namespace=namespace, name=configmap_name, body=body)
            logger.info(f"Replaced ConfigMap '{configmap_name}' for dashboard {dashboard_id} in '{namespace}'")
        else:
            raise

    return configmap_name


def _download_dashboard_from_grafana_com(dashboard_id: int) -> dict:
    """Download dashboard JSON from the Grafana.com API."""
    url = f"https://grafana.com/api/dashboards/{dashboard_id}/revisions/latest/download"
    logger.info(f"Downloading dashboard {dashboard_id} from {url}")
    req = urllib.request.Request(url, headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode("utf-8"))


def _import_dashboard(
    dashboard_json: dict,
    grafana_url: str,
    grafana_user: str,
    grafana_password: str,
    datasource_name: str,
    folder_id: int,
) -> dict:
    """POST a dashboard JSON to the Grafana import API."""
    # Reset the dashboard ID and UID so Grafana treats it as a new import
    dashboard_json = dict(dashboard_json)
    dashboard_json["id"] = None

    payload = json.dumps(
        {
            "dashboard": dashboard_json,
            "overwrite": True,
            "folderId": folder_id,
            "inputs": [
                {
                    "name": "__DS_PROMETHEUS",
                    "type": "datasource",
                    "pluginId": "prometheus",
                    "value": datasource_name,
                }
            ],
        }
    ).encode("utf-8")

    url = f"{grafana_url.rstrip('/')}/api/dashboards/import"
    credentials = f"{grafana_user}:{grafana_password}"
    import base64
    auth_header = "Basic " + base64.b64encode(credentials.encode()).decode()

    req = urllib.request.Request(
        url,
        data=payload,
        headers={
            "Content-Type": "application/json",
            "Authorization": auth_header,
        },
        method="POST",
    )

    logger.info(f"Importing dashboard to Grafana at {url}")
    with urllib.request.urlopen(req, timeout=30) as resp:
        result = json.loads(resp.read().decode("utf-8"))

    status = result.get("status")
    if status not in ("success", "imported"):
        raise RuntimeError(f"Grafana dashboard import failed: {result}")

    logger.info(f"Dashboard imported successfully: {result.get('slug', result.get('uid', ''))}")
    return result
