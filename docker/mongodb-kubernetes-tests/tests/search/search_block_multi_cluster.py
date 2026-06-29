"""E2E: a single operator rejects multi-cluster MongoDBSearch when the block is on.

A single-operator install cannot manage a multi-cluster (>1 entries) search deployment,
so such a spec must fail fast with a "not supported yet" status before any source
handling. No MongoDB source is deployed — the block fires before source resolution, so
the source reference is irrelevant.
"""

from __future__ import annotations

import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search.bootstrap_test_mixins import InstallOperatorTests
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_block_multi_cluster

NAMESPACE = get_namespace()

# workflow.Invalid capitalizes the first char, so match from "MongoDBSearch".
BLOCK_MSG_RE = r".*MongoDBSearch is not supported yet.*"

# More than one cluster entry: passes CRD admission (each entry has name + index) but
# the single operator in this install (no operatorClusterName) rejects it.
TWO_CLUSTERS = [
    {"name": "cluster-0", "index": 0},
    {"name": "cluster-1", "index": 1},
]


def assert_multi_cluster_is_blocked(fixture_name: str, mdbs_name: str):
    """Apply a >1-cluster spec to a source-specific MongoDBSearch fixture and assert the
    single operator fails it fast."""
    resource = MongoDBSearch.from_yaml(yaml_fixture(fixture_name), namespace=NAMESPACE, name=mdbs_name)
    try_load(resource)
    resource["spec"]["clusters"] = TWO_CLUSTERS
    resource.update()
    resource.assert_reaches_phase(Phase.Failed, msg_regexp=BLOCK_MSG_RE, timeout=120)


class TestInstallOperator(InstallOperatorTests):
    pass


def test_replica_set_source_blocked():
    assert_multi_cluster_is_blocked("search-rs-managed-lb.yaml", "mdb-rs-block")


def test_sharded_source_blocked():
    assert_multi_cluster_is_blocked("search-sharded-external-mongod.yaml", "mdb-sh-block")
