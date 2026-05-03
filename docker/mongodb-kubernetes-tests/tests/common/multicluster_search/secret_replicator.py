"""Cross-cluster Secret replicator for the MC search e2e harness.

Copies a Secret from the central cluster to each named member cluster.
Idempotent: if the Secret already exists in a member cluster with matching
data, no API call is made; if it exists with different data, the Secret is
patched; if it does not exist, it is created.

This is a TEST-ONLY utility. The MCK operator does NOT replicate Secrets in
production — that is the customer's responsibility per program rules. The
harness exists so e2e tests can stand up a working multi-cluster fixture
without requiring the test runner to mirror the production replication
machinery.
"""

from typing import Mapping

from kubernetes.client import CoreV1Api, V1ObjectMeta, V1Secret
from kubernetes.client.exceptions import ApiException
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


def replicate_secret(
    secret_name: str,
    namespace: str,
    central_client: CoreV1Api,
    member_clients: Mapping[str, CoreV1Api],
) -> None:
    """Replicate `secret_name` from `central_client` into every cluster in `member_clients`.

    Args:
        secret_name: Name of the Secret to replicate.
        namespace: Namespace in which the Secret lives in the central cluster
            and should be created in each member cluster.
        central_client: kubernetes CoreV1Api for the central cluster (the
            authoritative source of the Secret).
        member_clients: mapping of cluster-name → kubernetes CoreV1Api for
            each member cluster the Secret should be replicated into.

    Raises:
        ApiException: re-raised from the central read if the source Secret
            does not exist; per-member errors are logged and re-raised.
    """
    source = central_client.read_namespaced_secret(name=secret_name, namespace=namespace)
    desired_data = dict(source.data or {})
    desired_type = source.type or "Opaque"

    for cluster_name, member in member_clients.items():
        try:
            existing = member.read_namespaced_secret(name=secret_name, namespace=namespace)
        except ApiException as exc:
            if exc.status != 404:
                logger.error(f"replicate_secret: read failed in cluster {cluster_name}: {exc}")
                raise
            existing = None

        if existing is None:
            body = V1Secret(
                metadata=V1ObjectMeta(name=secret_name, namespace=namespace),
                type=desired_type,
                data=desired_data,
            )
            member.create_namespaced_secret(namespace=namespace, body=body)
            logger.info(f"replicate_secret: created {secret_name} in cluster {cluster_name}")
            continue

        if (existing.data or {}) == desired_data:
            logger.debug(f"replicate_secret: {secret_name} in cluster {cluster_name} already up to date")
            continue

        body = V1Secret(
            metadata=V1ObjectMeta(name=secret_name, namespace=namespace),
            type=desired_type,
            data=desired_data,
        )
        member.patch_namespaced_secret(name=secret_name, namespace=namespace, body=body)
        logger.info(f"replicate_secret: patched {secret_name} in cluster {cluster_name}")
