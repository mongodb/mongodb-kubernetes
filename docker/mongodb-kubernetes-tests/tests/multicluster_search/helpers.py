"""Shared helpers for the Q2-MC search e2e scaffold."""

from typing import List

from kubernetes import client
from kubetester.kubetester import run_periodically
from kubetester.multicluster_client import MultiClusterClient
from tests import test_logger
from tests.common.search import search_resource_names

logger = test_logger.get_test_logger(__name__)


def build_clusters_hosts_spec(
    member_cluster_clients: List[MultiClusterClient],
    hosts: List[str],
    replicas: int = 2,
) -> List[dict]:
    """Produce one spec.clusters[] entry per member cluster pinned to `hosts`.

    Hosts-first MVP routing path (CLARIFY-6 + CLARIFY-8). The same host list
    is shared across all member clusters; per-cluster fan-out is a Phase 5
    implementation concern (operator code, not the test scaffold).
    """
    return [
        {
            "clusterName": mcc.cluster_name,
            "replicas": replicas,
            "syncSourceSelector": {"hosts": hosts},
        }
        for mcc in member_cluster_clients
    ]


def _envoy_ready_in_cluster(namespace: str, deployment_name: str, mcc: MultiClusterClient) -> tuple[bool, str]:
    """Check whether the Envoy Deployment in a single cluster has at least one ready replica."""
    apps_v1 = client.AppsV1Api(api_client=mcc.api_client)
    try:
        deployment = apps_v1.read_namespaced_deployment(deployment_name, namespace)
        ready = deployment.status.ready_replicas or 0
        return ready >= 1, f"cluster={mcc.cluster_name} deployment={deployment_name} ready_replicas={ready}"
    except Exception as e:
        return False, f"cluster={mcc.cluster_name} deployment {deployment_name} not found: {e}"


def assert_envoy_ready_in_each_cluster(
    namespace: str,
    mdbs_name: str,
    member_cluster_clients: List[MultiClusterClient],
    timeout: int = 180,
):
    """Poll all member clusters concurrently until each per-cluster Envoy Deployment is ready.

    The per-cluster Deployment name is `{mdbs_name}-search-lb-0-{clusterName}` (B16) so each
    member cluster is queried for its own Deployment, not a shared name.
    """

    def all_ready():
        statuses = [
            _envoy_ready_in_cluster(
                namespace,
                search_resource_names.lb_deployment_name_for_cluster(mdbs_name, mcc.cluster_name),
                mcc,
            )
            for mcc in member_cluster_clients
        ]
        ok = all(ready for ready, _ in statuses)
        msg = "; ".join(detail for _, detail in statuses)
        return ok, msg

    run_periodically(
        all_ready,
        timeout=timeout,
        sleep_time=5,
        msg=f"per-cluster Envoy Deployments for MongoDBSearch {mdbs_name} ready in all member clusters",
    )
    logger.info(f"Envoy Deployment ready on all {len(member_cluster_clients)} cluster(s)")
