"""Per-cluster resource and pod readiness assertion helpers.

These are thin pytest assertion wrappers around the kubernetes Python
client. They exist as named helpers (rather than inline `assert`s)
so failure messages name the cluster + resource clearly when they fire.
"""

from kubernetes.client import AppsV1Api, CoreV1Api
from kubernetes.client.exceptions import ApiException

# Maps resource kind to the CoreV1Api / AppsV1Api method name that fetches it.
_KIND_METHOD: dict[str, str] = {
    "Service": "read_namespaced_service",
    "ConfigMap": "read_namespaced_config_map",
    "Secret": "read_namespaced_secret",
    "StatefulSet": "read_namespaced_stateful_set",
    "Deployment": "read_namespaced_deployment",
}


def assert_deployment_ready_in_cluster(apps: AppsV1Api, *, name: str, namespace: str) -> None:
    """Assert Deployment `name`/`namespace` has all spec.replicas ready."""
    dep = apps.read_namespaced_deployment(name=name, namespace=namespace)
    ready = dep.status.ready_replicas or 0
    desired = dep.spec.replicas or 0
    if ready != desired or desired == 0:
        raise AssertionError(f"Deployment {namespace}/{name}: ready_replicas={ready}/{desired}")


def assert_resource_in_cluster(client, *, kind: str, name: str, namespace: str) -> None:
    """Assert that a resource of `kind`/`name` exists in `namespace`.

    Supported kinds: Service, ConfigMap, Secret, StatefulSet, Deployment.
    """
    if kind not in _KIND_METHOD:
        raise ValueError(f"unsupported kind: {kind}")
    try:
        getattr(client, _KIND_METHOD[kind])(name=name, namespace=namespace)
    except ApiException as exc:
        if exc.status == 404:
            raise AssertionError(f"{kind} {namespace}/{name} not found in target cluster")
        raise
