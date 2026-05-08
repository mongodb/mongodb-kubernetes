"""Per-cluster routing-attribution e2e for MC RS search (KUBE-17, observability).

Split out from ``tests/multicluster_search/search_connectivity_tool_mc_rs.py``
because these two tests assert *which cluster's mongot* served a query by
parsing per-cluster mongot interceptor logs via
``assert_search_command_landed_in_cluster``. That dependency was removed from
the auto-executed connectivity-tool suite — these tests stay available for
manual/observability runs under the ``e2e_search_observability_mc_per_cluster_routing``
marker but are not wired into any Evergreen variant.

The structural same-cluster-routing invariant (per-cluster ``mongotHost`` AC
patch + per-cluster envoy SNI) is still proved by ``test_per_cluster_mongot_host_observed``
and ``test_per_cluster_envoy_sni_observed`` in the bootstrap mixin, which the
auto-executed MC suite runs unchanged.
"""

from __future__ import annotations

from datetime import datetime, timezone
from typing import List

import pytest
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_search import MongoDBSearch
from kubetester.multicluster_client import MultiClusterClient
from tests import test_logger
from tests.common.search.bootstrap_test_mixins import SearchSampleDataAndIndexTests, _derive_user_defaults
from tests.common.search.bootstrap_test_mixins_mc import (
    MongoDBMultiRsDeploymentConfig,
    MongoDBMultiRsDeploymentTests,
    SearchMCDeploymentTests,
    SearchMCE2EFixtures,
)
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.mc_search_helper import assert_search_command_landed_in_cluster, mongot_pod_prefix_for_cluster
from tests.common.search.rs_search_helper import get_mc_rs_search_tester_for_cluster_member
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_observability_mc_per_cluster_routing


def configure_mongodb_mc_rs_config(cfg: MongoDBMultiRsDeploymentConfig) -> MongoDBMultiRsDeploymentConfig:
    cfg.mdb_resource_name = "mdb-mc-rs-conn-tool"
    cfg.admin_user_name = ""
    cfg.admin_user_password = ""
    cfg.user_name = ""
    cfg.user_password = ""
    _derive_user_defaults(cfg)
    return cfg


class TestSearchWithMongoDBMulti(
    SearchMCDeploymentTests,
    MongoDBMultiRsDeploymentTests,
):
    def build_mongodb_mc_rs_config(self) -> MongoDBMultiRsDeploymentConfig:
        return configure_mongodb_mc_rs_config(super().build_mongodb_mc_rs_config())


class TestSearchSampleDataAndIndex(
    SearchSampleDataAndIndexTests,
    SearchMCE2EFixtures,
):
    def build_mongodb_mc_rs_config(self) -> MongoDBMultiRsDeploymentConfig:
        return configure_mongodb_mc_rs_config(super().build_mongodb_mc_rs_config())


class TestSearchPerClusterRoutingAttribution(SearchMCE2EFixtures):
    def build_mongodb_mc_rs_config(self) -> MongoDBMultiRsDeploymentConfig:
        return configure_mongodb_mc_rs_config(super().build_mongodb_mc_rs_config())

    def test_per_cluster_direct_oneshot_search(
        self,
        mdb: MongoDBMulti,
        mdbs: MongoDBSearch,
        namespace: str,
        member_cluster_clients: List[MultiClusterClient],
        helper: MCSearchDeploymentHelper,
    ):
        """For each cluster N: direct-connect to cluster N's mongod-0, run one-shot
        search, and assert a SearchCommand record lands in cluster N's mongot logs.
        """
        cfg = self.build_mongodb_mc_rs_config()
        for mcc in member_cluster_clients:
            cluster_index = helper.cluster_index(mcc.cluster_name)
            tester = get_mc_rs_search_tester_for_cluster_member(
                mdb,
                cluster_index,
                0,
                cfg.user_name,
                cfg.user_password,
            )
            tool = SearchConnectivityTool(tester)

            t0 = datetime.now(timezone.utc)
            result = tool.oneshot_search()
            logger.info(f"cluster {cluster_index} ({mcc.cluster_name}) oneshot_search: {result}")
            assert result.success, (
                f"cluster {cluster_index}: one-shot search failed: " f"{result.error_class} {result.error_message}"
            )
            assert result.returned_count > 0, f"cluster {cluster_index}: expected results"

            since_seconds = int((datetime.now(timezone.utc) - t0).total_seconds()) + 5
            assert_search_command_landed_in_cluster(
                namespace=namespace,
                cluster_core_v1=mcc.core_v1_api(),
                cluster_api_client=mcc.api_client,
                mongot_pod_prefix=mongot_pod_prefix_for_cluster(mdbs.name, cluster_index),
                since_seconds=since_seconds,
                cluster_label=f"{mcc.cluster_name}/idx={cluster_index}",
            )

    def test_per_cluster_direct_paging_search(
        self,
        mdb: MongoDBMulti,
        mdbs: MongoDBSearch,
        namespace: str,
        member_cluster_clients: List[MultiClusterClient],
        helper: MCSearchDeploymentHelper,
    ):
        """Same per-cluster sweep as the one-shot variant but via paging_search."""
        cfg = self.build_mongodb_mc_rs_config()
        for mcc in member_cluster_clients:
            cluster_index = helper.cluster_index(mcc.cluster_name)
            tester = get_mc_rs_search_tester_for_cluster_member(
                mdb,
                cluster_index,
                0,
                cfg.user_name,
                cfg.user_password,
            )
            tool = SearchConnectivityTool(tester)

            t0 = datetime.now(timezone.utc)
            pages = tool.paging_search(pages=3, interval_seconds=0.1, batch_size=20)
            logger.info(
                "cluster %d (%s) paging_search: %s",
                cluster_index,
                mcc.cluster_name,
                "; ".join(str(p) for p in pages),
            )
            assert pages, f"cluster {cluster_index}: paging_search returned no pages"
            assert pages[0].success, (
                f"cluster {cluster_index}: first page failed: " f"{pages[0].error_class} {pages[0].error_message}"
            )
            assert pages[0].returned_count > 0, f"cluster {cluster_index}: first page returned 0 docs"

            since_seconds = int((datetime.now(timezone.utc) - t0).total_seconds()) + 5
            assert_search_command_landed_in_cluster(
                namespace=namespace,
                cluster_core_v1=mcc.core_v1_api(),
                cluster_api_client=mcc.api_client,
                mongot_pod_prefix=mongot_pod_prefix_for_cluster(mdbs.name, cluster_index),
                since_seconds=since_seconds,
                cluster_label=f"{mcc.cluster_name}/idx={cluster_index}",
            )
