"""Shared helpers for the Q2-MC search e2e scaffold.

Kept package-local because they are scaffold-only and will move into
`tests/common/search/` once the multi-cluster contract solidifies and a
real helper module emerges.
"""

from typing import List

from kubernetes import client
from kubetester.kubetester import run_periodically
from kubetester.multicluster_client import MultiClusterClient
from tests import test_logger
from tests.common.search import search_resource_names

logger = test_logger.get_test_logger(__name__)

# Region tags assigned per cluster index. Long enough to cover the largest
# realistic kind harness (3 member clusters); extra entries are unused.
REGION_TAGS = ["us-east", "us-west", "eu-central"]


def build_clusters_spec(member_cluster_clients: List[MultiClusterClient], replicas: int = 2) -> List[dict]:
    """Produce one spec.clusters[] entry per member cluster, region-tagged."""
    return [
        {
            "clusterName": mcc.cluster_name,
            "replicas": replicas,
            "syncSourceSelector": {"matchTags": {"region": REGION_TAGS[idx % len(REGION_TAGS)]}},
        }
        for idx, mcc in enumerate(member_cluster_clients)
    ]


def assert_envoy_ready_in_each_cluster(
    namespace: str,
    mdbs_name: str,
    member_cluster_clients: List[MultiClusterClient],
    timeout: int = 180,
):
    """Per-cluster Envoy distribution lands in PR #1036; the deployment name
    is taken from the existing helper and will need updating if #1036
    introduces a per-cluster naming convention.
    """
    envoy_deployment_name = search_resource_names.lb_deployment_name(mdbs_name)

    for mcc in member_cluster_clients:
        apps_v1 = client.AppsV1Api(api_client=mcc.api_client)

        def check():
            try:
                deployment = apps_v1.read_namespaced_deployment(envoy_deployment_name, namespace)
                ready = deployment.status.ready_replicas or 0
                return ready >= 1, f"cluster={mcc.cluster_name} ready_replicas={ready}"
            except Exception as e:
                return False, f"cluster={mcc.cluster_name} deployment {envoy_deployment_name} not found: {e}"

        run_periodically(
            check,
            timeout=timeout,
            sleep_time=5,
            msg=f"Envoy Deployment {envoy_deployment_name} on cluster {mcc.cluster_name}",
        )
        logger.info(f"Envoy Deployment ready on cluster {mcc.cluster_name}")
