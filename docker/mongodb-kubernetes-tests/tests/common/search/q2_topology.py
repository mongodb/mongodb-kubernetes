"""Cluster/region constants and per-cluster status helpers for Q2-MC MongoDBSearch e2e tests.

Single source of truth for cluster identifiers and region tags used by
both single-cluster scaffolds and the multi-cluster harness, so SC and MC
stay in sync.
"""

from typing import Optional

from kubetester.mongodb_search import MongoDBSearch
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

# Region tags assigned per cluster index. Long enough to cover the largest
# realistic kind harness (3 member clusters); extra entries are unused.
REGION_TAGS = ["us-east", "us-west", "eu-central"]

# Cluster identifier used by single-cluster scaffolds. Matches the legacy
# central-cluster-only naming so the auto-promotion path lands here.
SINGLE_CLUSTER_NAME = "kind-e2e-cluster-1"

# Region tag for single-cluster scaffolds — pinned to REGION_TAGS[0] to avoid drift.
SINGLE_REGION_TAG = REGION_TAGS[0]


def get_cluster_statuses(mdbs: MongoDBSearch) -> Optional[list]:
    """Return clusterStatusList.clusterStatuses if the operator wrote it; None otherwise.

    The per-cluster status surface (`status.clusterStatusList`) is wired by B9
    (KUBE Phase 2 status writes). Until that lands on master, the field is
    absent — tests must tolerate this and skip assertions rather than KeyError.
    """
    mdbs.load()
    if "status" not in mdbs:
        return None
    status = mdbs["status"] or {}
    cluster_status_list = status.get("clusterStatusList") or {}
    return cluster_status_list.get("clusterStatuses")
