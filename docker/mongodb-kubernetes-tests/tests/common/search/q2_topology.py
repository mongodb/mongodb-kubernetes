"""Cluster/region constants for Q2-MC MongoDBSearch e2e tests.

Single source of truth for cluster identifiers and region tags used by
both single-cluster scaffolds and the multi-cluster harness, so SC and MC
stay in sync.
"""

# Region tags assigned per cluster index. Long enough to cover the largest
# realistic kind harness (3 member clusters); extra entries are unused.
REGION_TAGS = ["us-east", "us-west", "eu-central"]

# Cluster identifier used by single-cluster scaffolds. Matches the legacy
# central-cluster-only naming so the auto-promotion path lands here.
SINGLE_CLUSTER_NAME = "kind-e2e-cluster-1"

# Region tag for single-cluster scaffolds — pinned to REGION_TAGS[0] to avoid drift.
SINGLE_REGION_TAG = REGION_TAGS[0]
