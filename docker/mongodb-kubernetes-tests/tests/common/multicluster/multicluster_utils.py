from typing import Mapping

from kubernetes.client import AppsV1Api, CoreV1Api, V1ObjectMeta, V1Secret
from kubernetes.client.exceptions import ApiException

from tests import test_logger

logger = test_logger.get_test_logger(__name__)


def multi_cluster_pod_names(replica_set_name: str, cluster_index_with_members: list[tuple[int, int]]) -> list[str]:
    """List of multi-cluster pod names for given replica set name and a list of member counts in member clusters."""
    result_list = []
    for cluster_index, members in cluster_index_with_members:
        result_list.extend([f"{replica_set_name}-{cluster_index}-{pod_idx}" for pod_idx in range(0, members)])

    return result_list


def multi_cluster_service_names(replica_set_name: str, cluster_index_with_members: list[tuple[int, int]]) -> list[str]:
    """List of multi-cluster service names for given replica set name and a list of member counts in member clusters."""
    return [f"{pod_name}-svc" for pod_name in multi_cluster_pod_names(replica_set_name, cluster_index_with_members)]


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
    """Assert a resource of `kind`/`name` exists in `namespace`.

    Supported kinds: Service, ConfigMap, Secret, StatefulSet, Deployment.
    `client` must be the typed API client for the resource's group (CoreV1Api
    for Service/ConfigMap/Secret, AppsV1Api for StatefulSet/Deployment).
    """
    if kind not in _KIND_METHOD:
        raise ValueError(f"unsupported kind: {kind}")
    try:
        getattr(client, _KIND_METHOD[kind])(name=name, namespace=namespace)
    except ApiException as exc:
        if exc.status == 404:
            raise AssertionError(f"{kind} {namespace}/{name} not found in target cluster")
        raise


def replicate_secret(
    secret_name: str,
    namespace: str,
    central_client: CoreV1Api,
    member_clients: Mapping[str, CoreV1Api],
) -> None:
    """Copy `secret_name` from `central_client` into every member cluster.

    Idempotent: creates if missing, patches if data differs, skips if matching.
    Test-only — production MCK does NOT replicate Secrets cross-cluster.
    """
    source = central_client.read_namespaced_secret(name=secret_name, namespace=namespace)
    desired_data = dict(source.data or {})
    desired_type = source.type or "Opaque"

    def _build_body() -> V1Secret:
        return V1Secret(
            metadata=V1ObjectMeta(name=secret_name, namespace=namespace),
            type=desired_type,
            data=desired_data,
        )

    for cluster_name, member in member_clients.items():
        try:
            existing = member.read_namespaced_secret(name=secret_name, namespace=namespace)
        except ApiException as exc:
            if exc.status != 404:
                logger.error(f"replicate_secret: read failed in cluster {cluster_name}: {exc}")
                raise
            existing = None

        if existing is None:
            member.create_namespaced_secret(namespace=namespace, body=_build_body())
            logger.info(f"replicate_secret: created {secret_name} in cluster {cluster_name}")
            continue

        if (existing.data or {}) == desired_data:
            logger.debug(f"replicate_secret: {secret_name} in cluster {cluster_name} already up to date")
            continue

        member.patch_namespaced_secret(name=secret_name, namespace=namespace, body=_build_body())
        logger.info(f"replicate_secret: patched {secret_name} in cluster {cluster_name}")
