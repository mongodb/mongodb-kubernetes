"""E2E test: Envoy retry policy retries failed requests on a different mongot host.

Proves that when a mongot pod is killed, requests that hit the dead pod are
transparently retried on a healthy mongot via the `previous_hosts` retry predicate.
Verification is two-pronged:
  1. All search queries succeed (no user-visible errors)
  2. Envoy upstream_rq_retry stat increments (retries actually happened)

Also proves Envoy sends mongot the `x-envoy-max-attempt-count` header (1 + numRetries)
that mongot's load shedding uses to decide when a rejection is the final attempt.
"""

from __future__ import annotations

import json
import time

import pytest
from kubetester.kubetester import run_periodically
from kubetester.mongodb_search import MongoDBSearch
from pytest import fixture
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.mongodb_tools_pod.mongodb_tools_pod import get_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBRsDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchRsDeploymentTests,
    SearchSampleDataAndIndexTests,
)
from tests.common.search.connectivity import SearchConnectivityTool, delete_pods
from tests.common.search.rs_search_helper import rs_search_tester
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_envoy_retry_policy

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-retry")
# 2 mongot replicas so envoy load-balances across them — when one dies,
# ~50% of requests hit the dead pod and must be retried.
SEARCH = SearchDeploymentConfig(mongot_replicas=2)
MDBS_NAME = MDB.mdb_resource_name
MONGOT_STS = search_resource_names.mongot_statefulset_name_for_cluster(MDBS_NAME)
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"
MAX_ATTEMPT_COUNT_HEADER = "x-envoy-max-attempt-count"


def _user_tool(namespace: str) -> SearchConnectivityTool:
    return SearchConnectivityTool(rs_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password))


def _wait_for_search_serving(tool: SearchConnectivityTool, timeout: float = 300.0) -> None:
    tool.wait_for_sentinel_indexed(timeout=timeout)


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBDeployment(MongoDBRsDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchDeployment(SearchRsDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSampleData(SearchSampleDataAndIndexTests):
    sample_config = SampleDataAndIndexConfig()

    def admin_tester(self, namespace: str):
        return rs_search_tester(MDBS_NAME, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return rs_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)


class TestEnvoyRetryPolicy:
    """Kill one mongot pod, burst requests, verify all succeed via retries."""

    @fixture(scope="module")
    def search_tools_pod(self, namespace: str, custom_mdb_version: str) -> mongodb_tools_pod.ToolsPod:
        return get_tools_pod(namespace, custom_mdb_version)

    def test_retry_on_pod_kill(
        self,
        namespace: str,
        search_tools_pod: mongodb_tools_pod.ToolsPod,
    ):
        tool = _user_tool(namespace)
        _wait_for_search_serving(tool)

        # Verify baseline: a few queries succeed before any disruption
        for i in range(5):
            result = tool.oneshot_search()
            assert result.success, f"baseline query {i} failed: {result.error_message}"
        logger.info("Baseline: 5 oneshot queries succeeded")

        # Get envoy pod IP for stats queries
        envoy_pod_ip = _get_envoy_pod_ip(namespace, MDBS_NAME)

        # Record initial retry count
        initial_retries = _get_upstream_retry_count(search_tools_pod, envoy_pod_ip)
        logger.info(f"Initial upstream_rq_retry count: {initial_retries}")

        # Start background tester BEFORE the kill — queries are already flowing
        # when the pod dies, maximizing the chance of hitting the dead endpoint.
        mongot_pods = _list_mongot_pods(namespace)
        assert len(mongot_pods) >= 2, f"need >= 2 mongot pods, got {len(mongot_pods)}"
        target_pod = mongot_pods[0]

        with SearchAvailabilityBackgroundTester(tool, mode="oneshot", interval_seconds=0.1) as bg:
            # Let queries flow for a moment to establish connections
            time.sleep(2)

            # Hard-kill ONE mongot pod while queries are in-flight
            logger.info(f"Hard-killing mongot pod: {target_pod.metadata.name}")
            delete_pods(
                namespace,
                label_selector=f"statefulset.kubernetes.io/pod-name={target_pod.metadata.name}",
                grace_period_seconds=0,
            )

            # Keep queries flowing for a few seconds after the kill — this is
            # the window where envoy still routes to the dead pod and retries.
            time.sleep(10)

        # Background tester stopped — examine results
        verdict = bg.verdict
        logger.info(f"Background tester verdict: {verdict.as_dict()}")

        # Check envoy retry stats
        post_retries = _get_upstream_retry_count(search_tools_pod, envoy_pod_ip)
        retries_during_test = post_retries - initial_retries
        logger.info(f"Envoy upstream_rq_retry: {initial_retries} -> {post_retries} (delta: {retries_during_test})")

        # All queries should have succeeded (retries are transparent to the client)
        assert verdict.failed == 0, (
            f"Expected zero failed queries (retry policy should handle failures transparently), "
            f"but got {verdict.failed} failures. error_breakdown={verdict.error_breakdown}"
        )
        assert verdict.succeeded > 0, "expected at least some successful queries"

        # Envoy must have retried at least some requests on a different host
        assert retries_during_test > 0, (
            f"Expected upstream_rq_retry to increment (proving retries happened), "
            f"but delta was 0. Either all requests hit the healthy pod or retries are not configured."
        )

        logger.info(
            f"Retry policy verified: {verdict.succeeded} queries succeeded, "
            f"{retries_during_test} retries performed by envoy"
        )

        # Wait for the killed pod to come back before test teardown
        _wait_for_mongot_ready(namespace, expected_count=2)
        logger.info("Mongot pod recovered — test complete")


class TestEnvoyMaxAttemptCountHeader:
    """Envoy sends x-envoy-max-attempt-count = 1 + numRetries on every mongot route."""

    @fixture(scope="module")
    def search_tools_pod(self, namespace: str, custom_mdb_version: str) -> mongodb_tools_pod.ToolsPod:
        return get_tools_pod(namespace, custom_mdb_version)

    def test_header_follows_num_retries(self, namespace: str, search_tools_pod: mongodb_tools_pod.ToolsPod):
        num_retries = 3
        expected = str(num_retries + 1)
        envoy_pod_ips = _get_envoy_pod_ips(namespace, MDBS_NAME)
        lds_updates_before = {ip: _get_lds_update_success(search_tools_pod, ip) for ip in envoy_pod_ips}

        mdbs = MongoDBSearch(name=MDBS_NAME, namespace=namespace).load()
        managed = mdbs["spec"]["clusters"][0]["loadBalancer"]["managed"]
        managed.setdefault("retryPolicy", {})["numRetries"] = num_retries
        mdbs.update()

        def header_loaded():
            values = _max_attempt_header_values(namespace, MDBS_NAME)
            if not values:
                return False, "no mongot routes in lds.json yet"
            if any(v != expected for v in values):
                return False, f"lds.json {MAX_ATTEMPT_COUNT_HEADER} values {values}, want all {expected!r}"
            # The LB ConfigMap reaches the envoy pod through the kubelet volume sync (up to ~1 min),
            # then envoy hot-reloads lds.json from disk and bumps listener_manager.lds.update_success.
            for ip, before in lds_updates_before.items():
                if _get_lds_update_success(search_tools_pod, ip) <= before:
                    return False, f"envoy {ip} has not reloaded lds.json yet"
            return True, f"all {len(envoy_pod_ips)} envoy pods loaded {MAX_ATTEMPT_COUNT_HEADER}={expected}"

        run_periodically(header_loaded, timeout=300, sleep_time=5, msg=f"envoy to send {MAX_ATTEMPT_COUNT_HEADER}")


def _get_envoy_pod_ip(namespace: str, search_name: str) -> str:
    """Return the pod IP of the first envoy pod."""
    from kubetester.kubetester import KubernetesTester

    deployment_name = search_resource_names.lb_deployment_name(search_name)
    pods = KubernetesTester.read_pod_labels(namespace, label_selector=f"app={deployment_name}")
    assert pods.items, f"no envoy pods found with label app={deployment_name}"
    pod = pods.items[0]
    logger.info(f"Envoy pod: {pod.metadata.name} (IP: {pod.status.pod_ip})")
    return pod.status.pod_ip


def _get_envoy_pod_ips(namespace: str, search_name: str) -> list[str]:
    """Return the pod IPs of all envoy pods."""
    from kubetester.kubetester import KubernetesTester

    deployment_name = search_resource_names.lb_deployment_name(search_name)
    pods = KubernetesTester.read_pod_labels(namespace, label_selector=f"app={deployment_name}")
    assert pods.items, f"no envoy pods found with label app={deployment_name}"
    return [p.status.pod_ip for p in pods.items]


def _max_attempt_header_values(namespace: str, search_name: str) -> list[str]:
    """Return the x-envoy-max-attempt-count value of every route in the LB ConfigMap's lds.json.

    Envoy's admin API only allows /ready, /stats, /drain_listeners and /logging, so
    config_dump is not reachable; lds.json is the exact file envoy loads.
    A route without the header contributes "<missing>" so the caller fails on it.
    """
    from kubetester.kubetester import KubernetesTester

    cm = KubernetesTester.read_configmap(namespace, search_resource_names.lb_configmap_name(search_name))
    values = []
    for route in _find_routes(json.loads(cm["lds.json"])):
        headers = {h["header"]["key"]: h["header"].get("value") for h in route.get("request_headers_to_add", [])}
        values.append(headers.get(MAX_ATTEMPT_COUNT_HEADER, "<missing>"))
    return values


def _get_lds_update_success(tools_pod: mongodb_tools_pod.ToolsPod, envoy_pod_ip: str) -> int:
    """Return envoy's listener_manager.lds.update_success counter."""
    output = tools_pod.run_command(["wget", "-qO-", f"http://{envoy_pod_ip}:9901/stats?filter=lds.update_success"])
    for line in output.splitlines():
        if line.startswith("listener_manager.lds.update_success:"):
            return int(line.split(":")[1].strip())
    return 0


def _find_routes(node) -> list[dict]:
    """Recursively collect every route entry under any virtual_hosts."""
    routes = []
    if isinstance(node, dict):
        for vh in node.get("virtual_hosts", []):
            routes.extend(vh.get("routes", []))
        for v in node.values():
            routes.extend(_find_routes(v))
    elif isinstance(node, list):
        for v in node:
            routes.extend(_find_routes(v))
    return routes


def _list_mongot_pods(namespace: str):
    """Return all mongot pods for the test's search deployment."""
    from kubetester import list_matching_pods

    pods = list_matching_pods(namespace, label_selector=MONGOT_SELECTOR)
    return [p for p in pods if p.status.phase == "Running"]


def _get_upstream_retry_count(tools_pod: mongodb_tools_pod.ToolsPod, envoy_pod_ip: str) -> int:
    """Sum all upstream_rq_retry counters across clusters from envoy stats."""
    output = tools_pod.run_command(["wget", "-qO-", f"http://{envoy_pod_ip}:9901/stats"])
    total = 0
    for line in output.splitlines():
        if "upstream_rq_retry:" in line and "cluster." in line:
            try:
                total += int(line.split(":")[1].strip())
            except (ValueError, IndexError):
                pass
    return total


def _wait_for_mongot_ready(namespace: str, expected_count: int, timeout: float = 180.0) -> None:
    """Wait until all mongot pods are Running and Ready."""
    from kubetester import wait_for_pods_ready

    wait_for_pods_ready(
        namespace,
        label_selector=MONGOT_SELECTOR,
        expected_count=expected_count,
        timeout=int(timeout),
    )
