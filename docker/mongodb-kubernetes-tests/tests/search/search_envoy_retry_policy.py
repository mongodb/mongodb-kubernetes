"""E2E test: Envoy retries failed requests on a different mongot host.

Proves that requests to a crashed but still routable mongot are transparently retried
on a healthy mongot via the `previous_hosts` retry predicate. The mongot JVM is
SIGKILLed in place: the pod keeps its IP and Envoy keeps the host until its next DNS
refresh, while the closed port turns every attempt into a retryable connect failure
or stream reset.
Verification is two-pronged:
  1. All search queries succeed (no user-visible errors)
  2. Envoy upstream_rq_retry stat increments (retries actually happened)
"""

from __future__ import annotations

import pytest
from kubernetes import client
from kubernetes.stream import stream
from kubetester.kubetester import run_periodically
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
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.rs_search_helper import rs_search_tester
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_envoy_retry_policy

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-retry")
# 2 mongot replicas so envoy load-balances across them — when one crashes,
# ~50% of requests hit the dead pod and must be retried.
SEARCH = SearchDeploymentConfig(mongot_replicas=2)
MDBS_NAME = MDB.mdb_resource_name
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"


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

    def search_clusters(self) -> list:
        clusters = super().search_clusters()
        clusters[0]["statefulSet"] = {
            "spec": {
                "template": {
                    "spec": {
                        # mongot's JVM is PID 1 otherwise, and the kernel ignores SIGKILL sent to a
                        # namespace init from inside its namespace.
                        "shareProcessNamespace": True,
                    }
                }
            }
        }
        return clusters

    def build_mdbs(self):
        mdbs = super().build_mdbs()
        mdbs["spec"]["clusters"][0]["loadBalancer"]["managed"]["retryPolicy"] = {"perTryTimeout": "1s"}
        return mdbs


class TestSampleData(SearchSampleDataAndIndexTests):
    sample_config = SampleDataAndIndexConfig()

    def admin_tester(self, namespace: str):
        return rs_search_tester(MDBS_NAME, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return rs_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)


class TestEnvoyRetryPolicy:
    """Crash one routable mongot in place and verify Envoy retries on the other host."""

    @fixture(scope="module")
    def search_tools_pod(self, namespace: str, custom_mdb_version: str) -> mongodb_tools_pod.ToolsPod:
        return get_tools_pod(namespace, custom_mdb_version)

    def test_retry_on_routable_mongot_failure(
        self,
        namespace: str,
        search_tools_pod: mongodb_tools_pod.ToolsPod,
    ):
        tool = _user_tool(namespace)
        _wait_for_search_serving(tool)
        _wait_for_mongot_ready(namespace, expected_count=SEARCH.mongot_replicas)

        for i in range(5):
            result = tool.oneshot_search()
            assert result.success, f"baseline query {i} failed: {result.error_message}"
        logger.info("Baseline: 5 oneshot queries succeeded")

        envoy_pod_ip = _get_envoy_pod_ip(namespace, MDBS_NAME)
        initial_retries = _get_upstream_retry_count(search_tools_pod, envoy_pod_ip)
        logger.info(f"Initial upstream_rq_retry count: {initial_retries}")

        mongot_pods = _list_mongot_pods(namespace)
        assert len(mongot_pods) == SEARCH.mongot_replicas, (
            f"expected {SEARCH.mongot_replicas} Running mongot pods, got "
            f"{len(mongot_pods)}: {[pod.metadata.name for pod in mongot_pods]}"
        )
        target_pod = sorted(mongot_pods, key=lambda pod: pod.metadata.name)[0]
        target_name = target_pod.metadata.name
        target_ip = target_pod.status.pod_ip
        initial_restarts = _mongot_restart_count(namespace, target_name)

        with SearchAvailabilityBackgroundTester(tool, mode="oneshot", interval_seconds=0.1) as bg:
            target_pid = _find_mongot_process_pid(namespace, target_name)
            _kill_mongot_process(namespace, target_name, target_pid)
            logger.info(f"SIGKILL sent to mongot pid={target_pid} in pod {target_name} ({target_ip})")

            def crashed() -> tuple[bool, str]:
                restarts = _mongot_restart_count(namespace, target_name)
                return restarts > initial_restarts, f"restartCount={initial_restarts}->{restarts}"

            run_periodically(crashed, timeout=30, sleep_time=0.2, msg=f"mongot container crash in {target_name}")

            post_retries = initial_retries

            def retry_observed() -> tuple[bool, str]:
                nonlocal post_retries
                post_retries = _get_upstream_retry_count(search_tools_pod, envoy_pod_ip)
                return post_retries > initial_retries, f"retries={initial_retries}->{post_retries}"

            run_periodically(
                retry_observed,
                timeout=30,
                sleep_time=0.5,
                msg=f"Envoy retry after routable mongot crash in {target_name} ({target_ip})",
            )

        verdict = bg.verdict
        logger.info(f"Background tester verdict: {verdict.as_dict()}")

        _wait_for_mongot_ready(namespace, expected_count=SEARCH.mongot_replicas, timeout=300.0)
        recovered_query = tool.oneshot_search()
        assert recovered_query.success, f"search failed after mongot restart: {recovered_query.error_message}"

        retries_during_test = post_retries - initial_retries
        logger.info(f"Envoy upstream_rq_retry: {initial_retries} -> {post_retries} (delta: {retries_during_test})")

        assert verdict.failed == 0, (
            f"Expected zero failed queries (retry policy should handle failures transparently), "
            f"but got {verdict.failed} failures. error_breakdown={verdict.error_breakdown}"
        )
        assert verdict.succeeded > 0, "expected at least some successful queries"
        logger.info(
            f"Retry policy verified: {verdict.succeeded} queries succeeded, "
            f"{retries_during_test} retries performed by envoy"
        )


def _mongot_exec(namespace: str, pod_name: str, script: str, *args: str) -> str:
    api = client.CoreV1Api()
    response = stream(
        api.connect_get_namespaced_pod_exec,
        pod_name,
        namespace,
        container="mongot",
        command=["sh", "-c", script, "mongot-control", *args],
        stderr=True,
        stdin=False,
        stdout=True,
        tty=False,
        _preload_content=False,
    )
    stdout_chunks = []
    stderr_chunks = []
    try:
        while response.is_open():
            response.update(timeout=1)
            if response.peek_stdout():
                stdout_chunks.append(response.read_stdout())
            if response.peek_stderr():
                stderr_chunks.append(response.read_stderr())
        stdout = "".join(stdout_chunks)
        stderr = "".join(stderr_chunks)
        return_code = response.returncode
    finally:
        response.close()
    if return_code != 0:
        raise RuntimeError(f"mongot command failed in pod {pod_name}: {stderr or stdout}")
    return stdout


_JAVA_PROCESS_CHECK = """
is_java() {
    read -r name < "/proc/$1/comm" 2>/dev/null || return 1
    executable=$(readlink "/proc/$1/exe" 2>/dev/null) || executable=
    case "$name:${executable##*/}" in java:*|*:java) return 0 ;; esac
    return 1
}
"""


def _find_mongot_process_pid(namespace: str, pod_name: str) -> int:
    script = (
        _JAVA_PROCESS_CHECK
        + """
found=
for dir in /proc/[0-9]*; do
    pid=${dir#/proc/}
    if is_java "$pid"; then
        [ -z "$found" ] || { echo "multiple java processes in mongot pod: $found $pid" >&2; exit 1; }
        found=$pid
    fi
done
[ -n "$found" ] || { echo "no java process visible in mongot pod" >&2; exit 1; }
[ "$found" != "1" ] || { echo "mongot java is PID 1; shareProcessNamespace is not in effect" >&2; exit 1; }
echo "pid:$found"
"""
    )
    output = _mongot_exec(namespace, pod_name, script)
    parts = output.strip().split(":")
    if len(parts) != 2 or parts[0] != "pid":
        raise RuntimeError(f"mongot pid was not identified in pod {pod_name}: {output}")
    return int(parts[1])


def _kill_mongot_process(namespace: str, pod_name: str, pid: int) -> None:
    # The kill is detached so this exec session returns before the container exits.
    script = (
        _JAVA_PROCESS_CHECK
        + """
pid=$1
is_java "$pid" || { echo "pid $pid is not the mongot java process" >&2; exit 1; }
(sleep 0.5; kill -KILL "$pid") >/dev/null 2>&1 </dev/null &
echo "scheduled:$pid"
"""
    )
    output = _mongot_exec(namespace, pod_name, script, str(pid))
    if output.strip() != f"scheduled:{pid}":
        raise RuntimeError(f"mongot SIGKILL was not scheduled in pod {pod_name}: {output}")


def _mongot_restart_count(namespace: str, pod_name: str) -> int:
    pod = client.CoreV1Api().read_namespaced_pod(name=pod_name, namespace=namespace)
    for status in pod.status.container_statuses or []:
        if status.name == "mongot":
            return status.restart_count
    raise AssertionError(f"mongot container status missing in pod {pod_name}")


def _get_envoy_pod_ip(namespace: str, search_name: str) -> str:
    """Return the pod IP of the first envoy pod."""
    from kubetester.kubetester import KubernetesTester

    deployment_name = search_resource_names.lb_deployment_name(search_name)
    pods = KubernetesTester.read_pod_labels(namespace, label_selector=f"app={deployment_name}")
    assert pods.items, f"no envoy pods found with label app={deployment_name}"
    pod = pods.items[0]
    logger.info(f"Envoy pod: {pod.metadata.name} (IP: {pod.status.pod_ip})")
    return pod.status.pod_ip


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
