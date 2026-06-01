"""E2E test: Envoy CDS hot-reload does not break active cursors.

Proves that filesystem xDS CDS reload (triggered by ConfigMap update)
does not disrupt in-flight gRPC streams. The test opens a paging cursor,
patches the CDS ConfigMap with a circuit breaker change, waits for
envoy to reload, then verifies the cursor still works.
"""

from __future__ import annotations

import json

import pytest
from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.mongodb_search import MongoDBSearch
from pytest import fixture
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester, assert_no_outage
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBShardedDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchShardedDeploymentTests,
    SearchShardedSampleDataAndIndex,
)
from tests.common.search.connectivity import SearchConnectivityTool, set_resource_disabled_annotation
from tests.common.search.sharded_search_helper import sharded_search_tester
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_cds_hot_reload

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-sh-cds-reload", shard_count=2)
SEARCH = SearchDeploymentConfig()
MDBS_NAME = MDB.mdb_resource_name


def _user_tool(namespace: str) -> SearchConnectivityTool:
    return SearchConnectivityTool(sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password))


def _load_mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
    resource.load()
    return resource


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBShardedDeployment(MongoDBShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchShardedDeployment(SearchShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSampleData(SearchShardedSampleDataAndIndex):
    mdb_config = MDB
    # ~70k corpus (≈21k restored + 50k synthetic) so the paging cursor spans many
    # cross-shard getMore round-trips while the CDS reload happens mid-stream.
    sample_config = SampleDataAndIndexConfig(extra_doc_count=50_000)

    def admin_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)


class TestSearchCDSHotReload:
    """Patch the envoy CDS ConfigMap mid-cursor; the reload must not break streams."""

    @fixture(scope="module")
    def search_tools_pod(self, namespace: str) -> mongodb_tools_pod.ToolsPod:
        return get_tools_pod(namespace)

    def test_cursor_survives_cds_hot_reload(
        self,
        namespace: str,
        search_tools_pod: mongodb_tools_pod.ToolsPod,
    ):
        tool = _user_tool(namespace)
        mdbs = _load_mdbs(namespace)
        _wait_for_search_serving(tool)

        # Open paging cursor, read pre-reload pages
        cursor = tool.paging_cursor_open(batch_size=10)
        try:
            pre_pages = tool.paging_cursor_read_pages(cursor, pages=3, interval_seconds=0.5, batch_size=10)
            assert all(p.success for p in pre_pages), f"pre-reload pages should all succeed: {pre_pages}"
            logger.info(f"Pre-reload: {len(pre_pages)} pages read successfully")

            # Disable reconciliation so the operator won't overwrite our ConfigMap patch
            set_resource_disabled_annotation(mdbs, True)

            # Record envoy pod identity before the reload
            envoy_pod = _get_envoy_pod(namespace, mdbs.name)
            pre_pod_name = envoy_pod.metadata.name
            pre_restart_count = _get_envoy_restart_count(envoy_pod)
            envoy_pod_ip = envoy_pod.status.pod_ip

            # Get current CDS reload count from envoy admin stats
            initial_cds_updates = _get_cds_update_count(search_tools_pod, envoy_pod_ip)
            logger.info(f"Initial CDS update_success count: {initial_cds_updates}")

            # Start background availability tester — continuously reads pages
            # throughout the disruption window to catch any transient blips.
            with SearchAvailabilityBackgroundTester(tool, mode="paging") as bg:
                # Patch CDS ConfigMap with modified circuit breaker
                cm_name = search_resource_names.lb_configmap_name(mdbs.name)
                cm_data = KubernetesTester.read_configmap(namespace, cm_name)
                patched_cds = _modify_cds_circuit_breaker(cm_data["cds.json"])
                logger.info(f"Patching ConfigMap {cm_name} with modified circuit breaker (max_connections doubled)")
                KubernetesTester.update_configmap(namespace, cm_name, {**cm_data, "cds.json": patched_cds})

                # Wait for Envoy to reload (poll stats until cds.update_success increments)
                _wait_for_cds_reload(search_tools_pod, envoy_pod_ip, initial_cds_updates)

                # Verify envoy pod was NOT restarted or replaced
                post_pod = _get_envoy_pod(namespace, mdbs.name)
                assert (
                    post_pod.metadata.name == pre_pod_name
                ), f"envoy pod was replaced: {pre_pod_name} -> {post_pod.metadata.name}"
                post_restart_count = _get_envoy_restart_count(post_pod)
                assert (
                    post_restart_count == pre_restart_count
                ), f"envoy pod restarted: restart_count {pre_restart_count} -> {post_restart_count}"
                logger.info(f"Envoy pod {pre_pod_name} stable (no restart, no rollout)")

            # Assert background tester saw zero failures during the entire window
            bg_verdict = bg.verdict
            logger.info(f"Background tester verdict: {bg_verdict.as_dict()}")
            assert_no_outage(bg_verdict)

            # Read more pages from the original cursor — cursor should still work
            post_pages = tool.paging_cursor_read_pages(
                cursor, pages=50, interval_seconds=0.5, batch_size=10, first_page_index=len(pre_pages)
            )
            verdict = tool.verdict(post_pages)
            logger.info(f"Post-reload verdict: {verdict.as_dict()}")
            assert verdict.failed == 0, f"cursor broken after CDS reload: {verdict.as_dict()}"
            assert verdict.succeeded > 0, "expected successful pages after reload"
        finally:
            cursor.close()
            set_resource_disabled_annotation(mdbs, False)


def _wait_for_search_serving(tool: SearchConnectivityTool, timeout: float = 300.0) -> None:
    tool.wait_for_sentinel_indexed(timeout=timeout)


def _get_envoy_pod(namespace: str, search_name: str):
    """Return the first envoy pod object."""
    deployment_name = search_resource_names.lb_deployment_name(search_name)
    pods = KubernetesTester.read_pod_labels(namespace, label_selector=f"app={deployment_name}")
    assert pods.items, f"no envoy pods found with label app={deployment_name}"
    pod = pods.items[0]
    logger.info(f"Envoy pod: {pod.metadata.name} (IP: {pod.status.pod_ip})")
    return pod


def _get_envoy_restart_count(pod) -> int:
    """Sum of restart counts across all containers in the pod."""
    return sum(cs.restart_count for cs in (pod.status.container_statuses or []))


def _get_cds_update_count(tools_pod: mongodb_tools_pod.ToolsPod, envoy_pod_ip: str) -> int:
    """Query envoy admin stats via the tools pod to get CDS update count."""
    output = tools_pod.run_command(["wget", "-qO-", f"http://{envoy_pod_ip}:9901/stats"])
    for line in output.splitlines():
        if "cluster_manager.cds.update_success" in line:
            return int(line.split(":")[1].strip())
    return 0


def _wait_for_cds_reload(
    tools_pod: mongodb_tools_pod.ToolsPod,
    envoy_pod_ip: str,
    initial_count: int,
    timeout: float = 180.0,
) -> None:
    # Poll envoy's /stats endpoint until cds.update_success exceeds initial_count.
    # that would mean that new config has been loaded by envoy

    def check():
        current = _get_cds_update_count(tools_pod, envoy_pod_ip)
        if current > initial_count:
            return True, f"CDS reloaded: {initial_count} -> {current}"
        return False, f"CDS update_success still at {current} (waiting for > {initial_count})"

    run_periodically(check, timeout=timeout, sleep_time=5, msg="Envoy CDS reload")


def _modify_cds_circuit_breaker(cds_json: str) -> str:
    """Parse CDS JSON and bump max_connections from 1024 to 2048 on all clusters.

    Actual CDS JSON structure (from ConfigMap):
    {
      "resources": [{
        "@type": "type.googleapis.com/envoy.config.cluster.v3.Cluster",
        "circuit_breakers": {
          "thresholds": [{
            "max_connections": 1024,
            "max_pending_requests": 1024,
            "max_requests": 1024,
            "max_retries": 3
          }]
        }
      }]
    }
    """
    cds = json.loads(cds_json)
    modified_count = 0
    for resource in cds.get("resources", []):
        thresholds = resource.get("circuit_breakers", {}).get("thresholds", [])
        for threshold in thresholds:
            current = threshold.get("max_connections", 1024)
            threshold["max_connections"] = current * 2
            modified_count += 1
    logger.info(f"Modified circuit breaker max_connections on {modified_count} threshold(s)")
    return json.dumps(cds, indent=2)
