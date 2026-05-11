"""Test helpers for multi-cluster MongoDBSearch e2e tests.

Consolidates per-cluster name lookups, Secret replication, and per-cluster
resource assertions into a single module so MC search tests share one
import surface. Production MCK does NOT replicate Secrets cross-cluster
(that is the customer's responsibility per program rules); the harness
below exists so e2e fixtures can stand up a working MC topology.
"""

from typing import Mapping

from kubernetes.client import AppsV1Api, CoreV1Api, V1ObjectMeta, V1Secret
from kubernetes.client.exceptions import ApiException

from tests import test_logger

logger = test_logger.get_test_logger(__name__)


class MCSearchDeploymentHelper:
    """Per-cluster index and proxy-svc FQDN lookups for MC search tests.

    Cluster ordering follows dict-insertion order of `member_cluster_clients`,
    which mirrors the operator's persisted ClusterMapping (the state ConfigMap
    assigns indexes in the order clusters first appear in spec.clusters[]).
    """

    def __init__(
        self,
        namespace: str,
        mdb_resource_name: str,
        mdbs_resource_name: str,
        member_cluster_clients: Mapping[str, CoreV1Api],
    ) -> None:
        self.namespace = namespace
        self.mdb_resource_name = mdb_resource_name
        self.mdbs_resource_name = mdbs_resource_name
        self._member_cluster_clients = dict(member_cluster_clients)
        self._cluster_indices = {name: idx for idx, name in enumerate(self._member_cluster_clients)}

    def member_cluster_names(self) -> list[str]:
        return list(self._member_cluster_clients.keys())

    def cluster_index(self, cluster_name: str) -> int:
        if cluster_name not in self._cluster_indices:
            raise KeyError(f"unknown member cluster: {cluster_name!r}")
        return self._cluster_indices[cluster_name]

    def member_clients(self) -> Mapping[str, CoreV1Api]:
        return self._member_cluster_clients

    def proxy_svc_fqdn(self, cluster_name: str) -> str:
        idx = self.cluster_index(cluster_name)
        return f"{self.mdbs_resource_name}-search-{idx}-proxy-svc.{self.namespace}.svc.cluster.local"


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
