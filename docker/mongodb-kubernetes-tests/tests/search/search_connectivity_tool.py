"""E2E tests for the search connectivity tool against a single-cluster RS."""

from __future__ import annotations

import time
from datetime import datetime, timezone
from typing import Optional

import pytest
from kubernetes import client
from kubetester import wait_for_pods_ready
from kubetester.kubetester import run_periodically
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import (
    SearchAvailabilityBackgroundTester,
    assert_no_outage,
    assert_outage_detected,
)
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBRsDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchRsDeploymentTests,
    SearchSampleDataAndIndexTests,
)
from tests.common.search.connectivity import (
    ConnectivityVerdict,
    SearchConnectivityTool,
    delete_pods,
    paging_baseline_and_fault,
    wait_for_all_pods_replaced,
    wait_for_mongot_statefulset_drained,
    wait_for_pods_by_label_replaced,
)
from tests.common.search.rs_search_helper import rs_search_tester, rs_search_tester_for_member
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_connectivity_tool

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-conn-tool")
SEARCH = SearchDeploymentConfig()
# Search CR name (defaults to the source MongoDB name) and its index-0 mongot STS.
MDBS_NAME = MDB.mdb_resource_name
MONGOT_STS = search_resource_names.mongot_statefulset_name_for_cluster(MDBS_NAME)
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"


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
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password)


class TestSearchConnectivityTool:
    def test_oneshot_search_succeeds(self, namespace: str):
        result = _user_connectivity_tool(namespace).oneshot_search()
        logger.info(f"oneshot_search result: {result}")
        assert result.success, f"one-shot search failed: {result.error_class} {result.error_message}"
        assert result.returned_count > 0, "expected results from cache-busted compound query"

    def test_paging_search_first_page_succeeds(self, namespace: str):
        pages = _user_connectivity_tool(namespace).paging_search(pages=3, interval_seconds=0.1, batch_size=20)
        logger.info("paging_search results: %s", "; ".join(str(p) for p in pages))
        assert pages, "paging_search returned no pages"
        assert pages[0].success, f"first page failed: {pages[0].error_class} {pages[0].error_message}"
        assert pages[0].returned_count > 0, "first page returned 0 docs"

    def test_paging_through_mongot_outage_surfaces_connectivity_error(self, namespace: str):
        tool = _user_connectivity_tool(namespace)

        pre_pages = tool.paging_search(pages=2, interval_seconds=0.1, batch_size=10)
        logger.info("pre-outage pages: %s", "; ".join(str(p) for p in pre_pages))
        assert any(p.success for p in pre_pages), f"no pre-outage successful page: {pre_pages}"

        mdbs = _load_mdbs(namespace)
        logger.info(f"setting MongoDBSearch {mdbs.name} spec.clusters[0].replicas -> 0 via operator")
        mdbs["spec"]["clusters"][0]["replicas"] = 0
        mdbs.update()
        wait_for_mongot_statefulset_drained(MONGOT_STS, namespace)

        post_pages = tool.paging_search(pages=20, interval_seconds=0.5, batch_size=10)
        logger.info("post-outage pages: %s", "; ".join(str(p) for p in post_pages))
        post_verdict = tool.verdict(post_pages)
        logger.info(f"post-outage verdict: {post_verdict.as_dict()}")
        assert (
            post_verdict.failed > 0
        ), f"post-outage verdict has no failures; tool isn't surfacing upstream loss: {post_verdict.as_dict()}"
        expected_error_classes = {
            "OperationFailure",
            "ServerSelectionTimeoutError",
            "NetworkTimeout",
            "AutoReconnect",
            "ConnectionFailure",
        }
        observed_error_classes = set(post_verdict.error_breakdown)
        assert (
            observed_error_classes & expected_error_classes
        ), f"no expected connectivity-class error in error_breakdown={post_verdict.error_breakdown}"

        logger.info(f"setting MongoDBSearch {mdbs.name} spec.clusters[0].replicas -> 1 via operator")
        mdbs["spec"]["clusters"][0]["replicas"] = 1
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=200)

    def test_paging_through_mongot_pod_restart_surfaces_lost_cursor(self, namespace: str):
        tool = _user_connectivity_tool(namespace)
        _wait_for_search_serving(tool, timeout=200.0)

        def fault() -> None:
            uids = delete_pods(namespace, label_selector=MONGOT_SELECTOR, grace_period_seconds=0)
            assert (
                len(uids) == 1
            ), f"expected exactly 1 mongot pod matching {MONGOT_SELECTOR}; got {len(uids)}: {list(uids)}"
            _wait_for_search_serving(tool, timeout=200.0)

        _, _, verdict = paging_baseline_and_fault(tool, fault_fn=fault)
        assert verdict.cursor_lost > 0, f"no cursor_lost failure surfaced after mongot pod restart: {verdict.as_dict()}"

    def test_mongot_graceful_shutdown_with_active_cursor_holds_to_grace_and_cursor_dies_with_kill(self, namespace: str):
        """SIGTERM with an actively-paged cursor is held until the kubelet
        grace boundary; cursor cannot survive the pod replacement.
        """
        tool = _user_connectivity_tool(namespace)

        _wait_for_search_serving(tool)
        old_pod = _single_mongot_pod(namespace, MONGOT_SELECTOR)
        grace = old_pod.spec.termination_grace_period_seconds or 30
        logger.info(f"mongot old pod={old_pod.metadata.name} grace={grace}s")

        cursor = tool.paging_cursor_open(batch_size=10)
        try:
            pre_sigterm_pages = tool.paging_cursor_read_pages(
                cursor,
                pages=2,
                interval_seconds=0.1,
                batch_size=10,
                first_page_index=0,
            )
            for pp in pre_sigterm_pages:
                logger.info(f"pre-replacement page {pp.page_index}: {pp!s}")

            t_sigterm = datetime.now(timezone.utc)
            captured_uids = delete_pods(namespace, label_selector=MONGOT_SELECTOR)

            t_old_exit = _page_until_mongot_terminates(
                tool=tool,
                cursor=cursor,
                namespace=namespace,
                old_pod=old_pod,
                watch_for=grace + 15,
                start_index=2,
            )
            assert t_old_exit is not None, "OLD mongot pod never terminated"
            elapsed = (t_old_exit - t_sigterm).total_seconds()
            logger.info(f"mongot OLD exited {elapsed:.2f}s after SIGTERM (grace={grace}s)")
            assert grace - 2 <= elapsed <= grace + 10, (
                f"OLD mongot pod exited at {elapsed:.2f}s post-SIGTERM; "
                f"expected within [grace-2, grace+10] = [{grace - 2}, {grace + 10}]s"
            )

            wait_for_all_pods_replaced(namespace, captured_uids, timeout=180)
            _wait_for_search_serving(tool)

            max_post_pages = 5000
            page_batch = 5
            page_base = 100
            post_verdict = ConnectivityVerdict()
            post_pages: list = []
            for batch_start in range(0, max_post_pages, page_batch):
                batch = tool.paging_cursor_read_pages(
                    cursor,
                    pages=page_batch,
                    interval_seconds=0.1,
                    batch_size=10,
                    first_page_index=page_base + batch_start,
                )
                post_pages.extend(batch)
                post_verdict = tool.verdict(post_pages)
                for pp in batch:
                    logger.info(f"post-replacement page {pp.page_index}: {pp!s}")
                if post_verdict.cursor_lost > 0 or post_verdict.failed > 0:
                    break
            logger.info(f"post-replacement verdict after {len(post_pages)} pages: {post_verdict.as_dict()}")
            assert post_verdict.cursor_lost > 0 or post_verdict.failed > 0, (
                f"expected cursor_lost / connectivity failure within {max_post_pages} "
                f"post-replacement pages; got verdict={post_verdict.as_dict()}"
            )
        finally:
            try:
                cursor.close()
            except Exception:
                pass

    def test_mongot_graceful_shutdown_exits_early_after_killcursors(self, namespace: str):
        """killCursors during grace lets mongot exit before grace expires."""
        tool = _user_connectivity_tool(namespace)

        _wait_for_search_serving(tool)
        old_pod = _single_mongot_pod(namespace, MONGOT_SELECTOR)
        grace = old_pod.spec.termination_grace_period_seconds or 30
        logger.info(f"mongot old pod={old_pod.metadata.name} grace={grace}s")

        cursor = tool.paging_cursor_open(batch_size=10)
        t_delete: datetime
        t_kill_cursors: datetime
        try:
            warmup_pages = tool.paging_cursor_read_pages(
                cursor,
                pages=2,
                interval_seconds=0.1,
                batch_size=10,
                first_page_index=0,
            )
            for pp in warmup_pages:
                logger.info(f"warmup page {pp.page_index}: {pp!s}")

            t_delete = datetime.now(timezone.utc)
            logger.info(f"deleting mongot pods {MONGOT_SELECTOR} (t_delete={t_delete.isoformat()})")
            captured_uids = delete_pods(namespace, label_selector=MONGOT_SELECTOR)

            logger.info(f"paging in-grace for 10s while OLD mongot is shutting down (grace={grace}s)")
            _page_for(tool, cursor, duration=10.0, sleep_between=0.5, start_index=2)
        finally:
            t_kill_cursors = datetime.now(timezone.utc)
            logger.info(f"closing cursor — pymongo issues killCursors (t_kill_cursors={t_kill_cursors.isoformat()})")
            try:
                cursor.close()
            except Exception as exc:
                logger.info(f"cursor.close() raised (ignored): {exc!r}")

        t_old_exit = _watch_mongot_termination(namespace, old_pod, watch_for=grace + 30)
        logger.info(f"OLD pod terminated at {t_old_exit.isoformat()}")
        wait_for_all_pods_replaced(namespace, captured_uids, timeout=120)

        elapsed_from_delete = (t_old_exit - t_delete).total_seconds()
        elapsed_from_killcursors = (t_old_exit - t_kill_cursors).total_seconds()
        logger.info(
            f"mongot OLD exited {elapsed_from_delete:.2f}s after delete, "
            f"{elapsed_from_killcursors:.2f}s after killCursors (grace={grace}s)"
        )
        assert elapsed_from_killcursors <= 10, (
            f"OLD exited {elapsed_from_killcursors:.2f}s after killCursors; "
            f"expected <= 10s (mongot should react to stream close)"
        )
        assert (
            elapsed_from_delete < grace - 5
        ), f"OLD exited {elapsed_from_delete:.2f}s after delete; expected < grace-5 = {grace - 5}s"

    def test_paging_through_envoy_restart_surfaces_lost_cursor(self, namespace: str):
        """Hard-kill envoy mid-cursor; getMore after envoy recovers must surface cursor_lost.

        Envoy pins each mongod→mongot HTTP/2 stream to a specific upstream
        connection. The old envoy pod takes that pinning with it; the
        replacement envoy opens a fresh connection that mongot doesn't
        recognize as the cursor's owner → RST_STREAM → cursor_lost.
        """
        tool = _user_connectivity_tool(namespace)
        envoy_selector = f"app={search_resource_names.lb_deployment_name(MDBS_NAME)}"
        logger.info(f"envoy-restart test: selector={envoy_selector} namespace={namespace}")

        _wait_for_search_serving(tool)

        captured_uids: dict[str, str] = {}

        def fault() -> None:
            killed = delete_pods(namespace, label_selector=envoy_selector, grace_period_seconds=0)
            captured_uids.update(killed)
            wait_for_pods_by_label_replaced(namespace, envoy_selector, captured_uids, timeout=180)
            _wait_for_search_serving(tool, timeout=200.0)

        _, _, verdict = paging_baseline_and_fault(tool, fault_fn=fault)
        assert (
            verdict.cursor_lost > 0
        ), f"no cursor_lost failure surfaced after envoy restart; got verdict={verdict.as_dict()}"

    def test_direct_secondary_paging_succeeds(self, namespace: str):
        _wait_for_search_serving(_user_connectivity_tool(namespace))

        tester = rs_search_tester_for_member(
            MDB.mdb_resource_name,
            namespace,
            member_index=1,
            username=MDB.user_name,
            password=MDB.user_password,
        )
        tool = SearchConnectivityTool(tester)

        pages = tool.paging_search(pages=5, interval_seconds=0.1, batch_size=10)
        logger.info("secondary pages: %s", "; ".join(str(p) for p in pages))
        assert pages, "no pages returned"
        assert all(
            p.success for p in pages
        ), f"secondary page failures: {[(p.page_index, p.error_class) for p in pages if not p.success]}"
        assert any(p.returned_count > 0 for p in pages), "no page returned docs from secondary"

    def test_direct_secondary_concurrent_with_primary_paging(self, namespace: str):
        primary_tool = _user_connectivity_tool(namespace)
        secondary_tool = SearchConnectivityTool(
            rs_search_tester_for_member(
                MDB.mdb_resource_name,
                namespace,
                member_index=1,
                username=MDB.user_name,
                password=MDB.user_password,
            )
        )

        _wait_for_search_serving(primary_tool)
        _wait_for_search_serving(secondary_tool, read_only=True)

        primary_cursor = primary_tool.paging_cursor_open(batch_size=10)
        secondary_cursor = secondary_tool.paging_cursor_open(batch_size=10)
        primary_pages = []
        secondary_pages = []
        try:
            for i in range(5):
                primary_pages.extend(
                    primary_tool.paging_cursor_read_pages(
                        primary_cursor,
                        pages=1,
                        interval_seconds=0.0,
                        batch_size=10,
                        first_page_index=i,
                    )
                )
                secondary_pages.extend(
                    secondary_tool.paging_cursor_read_pages(
                        secondary_cursor,
                        pages=1,
                        interval_seconds=0.0,
                        batch_size=10,
                        first_page_index=i,
                    )
                )
        finally:
            for cur in (primary_cursor, secondary_cursor):
                cur.close()

        pv = primary_tool.verdict(primary_pages)
        sv = secondary_tool.verdict(secondary_pages)
        logger.info(f"primary verdict: {pv.as_dict()}")
        logger.info(f"secondary verdict: {sv.as_dict()}")
        assert pv.failed == 0, f"primary had failures: {pv.as_dict()}"
        assert sv.failed == 0, f"secondary had failures: {sv.as_dict()}"


# Background connectivity helper

DRAIN_MIN_PAGES = 100


class TestSearchConnectivityBackgroundTester:
    @staticmethod
    def new_one_shot_background_tester(
        namespace: str, user_name: str, user_password: str
    ) -> SearchAvailabilityBackgroundTester:
        tool = SearchConnectivityTool(rs_search_tester(MDB.mdb_resource_name, namespace, user_name, user_password))
        return SearchAvailabilityBackgroundTester(tool, mode="oneshot")

    @staticmethod
    def new_paging_background_tester_pinned_to_one_mongot(
        namespace: str, user_name: str, user_password: str, interval_seconds: float = 0.0
    ) -> SearchAvailabilityBackgroundTester:
        tool = SearchConnectivityTool(rs_search_tester(MDB.mdb_resource_name, namespace, user_name, user_password))
        return SearchAvailabilityBackgroundTester(
            tool, mode="paging", paging_reset_every=None, interval_seconds=interval_seconds
        )

    def test_scale_up_down_mongot_pods_without_outage(self, namespace: str):
        mdbs = _load_mdbs(namespace)
        mdbs.assert_reaches_phase(Phase.Running)
        initial_replicas_count = mdbs["spec"]["clusters"][0]["replicas"]
        pod_selector = MONGOT_SELECTOR
        # Per-event drain target: paging batch_size=5 × 200 pages = 1000 docs
        # past the scale event — enough that the paging cursor's prefetch
        # buffer is reissued at least once via real getMore round-trips.
        drain_timeout = 180.0
        # oneshot tester will not be pinned by design, so scaled up mongot nodes should work correctly as soon as those are ready
        with TestSearchConnectivityBackgroundTester.new_one_shot_background_tester(
            namespace, MDB.user_name, MDB.user_password
        ) as oneshot_tester:
            # paging background tester will use one of the replicas that existed before the test was executed
            # all the queries should be paging through that node and adding+removing new mongot node should not affect existing queries
            with TestSearchConnectivityBackgroundTester.new_paging_background_tester_pinned_to_one_mongot(
                namespace, MDB.user_name, MDB.user_password, interval_seconds=0.1
            ) as paging_tester:
                # warm both testers — first iteration sets up cursors and routes.
                paging_tester.wait_for_operations(5)
                oneshot_tester.wait_for_operations(5)

                # scale up: add a new mongot replica. Existing paging cursor must
                # stay pinned to its original mongot; oneshot must keep routing.
                mdbs["spec"]["clusters"][0]["replicas"] = initial_replicas_count + 1
                mdbs.update()
                mdbs.assert_reaches_phase(Phase.Running)
                wait_for_pods_ready(
                    mdbs.namespace, label_selector=pod_selector, expected_count=initial_replicas_count + 1
                )

                # Drain enough post-scale pages to be confident the cursor's
                # getMore hit mongot at least once — proves availability through
                # the scale-up, not just buffered docs.
                paging_tester.wait_for_operations(DRAIN_MIN_PAGES, timeout=drain_timeout)

                # scale down: remove the extra mongot. Cursor must survive.
                mdbs["spec"]["clusters"][0]["replicas"] = initial_replicas_count
                mdbs.update()
                mdbs.assert_reaches_phase(Phase.Running)
                wait_for_pods_ready(mdbs.namespace, label_selector=pod_selector, expected_count=initial_replicas_count)

                paging_tester.wait_for_operations(DRAIN_MIN_PAGES, timeout=drain_timeout)

        oneshot_verdict = oneshot_tester.verdict
        paging_verdict = paging_tester.verdict
        logger.info(f"no-outage oneshot verdict: {oneshot_verdict.as_dict()}")
        logger.info(f"no-outage paging verdict: {paging_verdict.as_dict()}")
        assert_no_outage(oneshot_verdict)
        assert_no_outage(paging_verdict)

    def test_mongot_pod_restart_surfaces_outage(self, namespace: str):
        # Matches the headless-service selector — catches every mongot replica.
        pod_selector = MONGOT_SELECTOR

        with TestSearchConnectivityBackgroundTester.new_one_shot_background_tester(
            namespace, MDB.user_name, MDB.user_password
        ) as oneshot_tester:
            with TestSearchConnectivityBackgroundTester.new_paging_background_tester_pinned_to_one_mongot(
                namespace, MDB.user_name, MDB.user_password
            ) as paging_tester:
                # Deterministic baseline — both testers must have read some
                # successful pages before the fault is applied.
                oneshot_tester.wait_for_operations(5)
                paging_tester.wait_for_operations(5)
                original_uids = delete_pods(namespace, label_selector=pod_selector, grace_period_seconds=0)
                try:
                    wait_for_pods_by_label_replaced(namespace, pod_selector, original_uids)
                except Exception as e:
                    logger.warning(f"mongot pod recreation wait timed out in cleanup: {e}")
                oneshot_tester.wait_for_operations(5, stop_on_fault=True)
                paging_tester.wait_for_operations(DRAIN_MIN_PAGES, stop_on_fault=True)

        oneshot_verdict = oneshot_tester.verdict
        paging_verdict = paging_tester.verdict
        logger.info(f"mongot-restart oneshot verdict: {oneshot_verdict.as_dict()}")
        logger.info(f"mongot-restart paging verdict: {paging_verdict.as_dict()}")
        assert_outage_detected(oneshot_verdict, accept_classes=("transient_network",))
        assert_outage_detected(paging_verdict, accept_classes=("cursor_lost", "transient_network"))

    def test_mongot_scale_to_zero_surfaces_network_error(self, namespace: str):
        with TestSearchConnectivityBackgroundTester.new_one_shot_background_tester(
            namespace, MDB.user_name, MDB.user_password
        ) as oneshot_tester:
            with TestSearchConnectivityBackgroundTester.new_paging_background_tester_pinned_to_one_mongot(
                namespace, MDB.user_name, MDB.user_password, interval_seconds=1.0
            ) as paging_tester:
                # Warm one page, then pause. Scaling mongot to zero takes 60s+; if the
                # background thread kept paging through that window mongod would prefetch
                # the whole result and serve it locally, masking the upstream loss.
                oneshot_tester.wait_for_operations(5)
                paging_tester.wait_for_operations(1)
                paging_tester.pause()

                mdbs = _load_mdbs(namespace)
                logger.info(f"scaling MongoDBSearch {mdbs.name} clusters[0].replicas -> 0")
                mdbs["spec"]["clusters"][0]["replicas"] = 0
                mdbs.update()
                wait_for_mongot_statefulset_drained(MONGOT_STS, namespace)

                # mongot is gone. Resume: a few pages may still serve from the buffer,
                # but the next getMore needing fresh data hits the dead upstream and faults.
                paging_tester.interval_seconds = 0.0
                paging_tester.resume()
                oneshot_tester.wait_for_operations(5, stop_on_fault=True)
                paging_tester.wait_for_operations(DRAIN_MIN_PAGES, stop_on_fault=True)

        mdbs = _load_mdbs(namespace)
        logger.info(f"scaling MongoDBSearch {mdbs.name} clusters[0].replicas -> 1")
        mdbs["spec"]["clusters"][0]["replicas"] = 1
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=300)

        oneshot_verdict = oneshot_tester.verdict
        paging_verdict = paging_tester.verdict
        logger.info(f"scale-to-zero oneshot verdict: {oneshot_verdict.as_dict()}")
        logger.info(f"scale-to-zero paging verdict: {paging_verdict.as_dict()}")
        assert_outage_detected(oneshot_verdict, accept_classes=("transient_network",))
        assert_outage_detected(paging_verdict, accept_classes=("transient_network",))


# Module-level helpers


def _wait_for_search_serving(
    tool: SearchConnectivityTool,
    *,
    timeout: float = 300.0,
    read_only: bool = False,
) -> None:
    """Block until mongot has indexed + served a freshly-inserted sentinel.

    A sentinel hit proves mongot picked up THIS doc and answered the query.
    ``read_only=True`` polls a $search instead — required for direct-secondary
    clients that can't accept writes.
    """
    if read_only:

        def search_returns():
            try:
                result = tool.oneshot_search(limit=1)
                if result.success:
                    return True, f"oneshot ok (returned={result.returned_count})"
                return False, "oneshot returned success=False"
            except Exception as exc:
                return False, f"{type(exc).__name__}: {exc}"

        run_periodically(
            search_returns, timeout=timeout, sleep_time=1.0, msg="secondary mongod→envoy→mongot path to serve $search"
        )
        return
    tool.wait_for_sentinel_indexed(timeout=timeout)


def _single_mongot_pod(namespace: str, label_selector: str):
    """Return the ordinal-0 mongot pod for ``label_selector`` (deterministic across rescales)."""
    from kubetester import list_matching_pods

    pods = [
        p
        for p in list_matching_pods(namespace, label_selector=label_selector)
        if p.metadata.name[p.metadata.name.rfind("-") + 1 :].isdigit()
    ]
    if not pods:
        raise AssertionError(f"expected at least 1 mongot pod matching {label_selector}, got 0")
    pods.sort(key=lambda p: p.metadata.name)
    return pods[0]


def _page_for(
    tool: SearchConnectivityTool,
    cursor,
    *,
    duration: float,
    sleep_between: float,
    start_index: int,
) -> None:
    deadline = time.monotonic() + duration
    idx = start_index
    while time.monotonic() < deadline:
        batch = tool.paging_cursor_read_pages(
            cursor,
            pages=1,
            interval_seconds=0.0,
            batch_size=10,
            first_page_index=idx,
        )
        for pp in batch:
            logger.info(f"in-grace page {pp.page_index}: {pp!s}")
        idx += 1
        time.sleep(sleep_between)


def _check_old_mongot_terminated(namespace: str, old_name: str, old_uid: str) -> Optional[datetime]:
    try:
        pod = client.CoreV1Api().read_namespaced_pod(name=old_name, namespace=namespace)
    except client.exceptions.ApiException as exc:
        if exc.status == 404:
            return datetime.now(timezone.utc)
        raise
    if pod.metadata.uid != old_uid:
        return datetime.now(timezone.utc)
    mongot_status = next(
        (cs for cs in (pod.status.container_statuses or []) if cs.name == "mongot"),
        None,
    )
    if mongot_status and mongot_status.state and mongot_status.state.terminated:
        return mongot_status.state.terminated.finished_at
    return None


def _page_until_mongot_terminates(
    *,
    tool: SearchConnectivityTool,
    cursor,
    namespace: str,
    old_pod,
    watch_for: float,
    start_index: int,
) -> Optional[datetime]:
    """Interleave page reads with pod-watch on the OLD mongot pod.

    Keeps the cursor active throughout so the stream stays in-flight — the
    condition under test.
    """
    old_name = old_pod.metadata.name
    old_uid = old_pod.metadata.uid
    deadline = time.monotonic() + watch_for
    idx = start_index
    while time.monotonic() < deadline:
        try:
            batch = tool.paging_cursor_read_pages(
                cursor,
                pages=1,
                interval_seconds=0.0,
                batch_size=10,
                first_page_index=idx,
            )
            for pp in batch:
                logger.info(f"pre-replacement page {pp.page_index}: {pp!s}")
            idx += 1
        except Exception as exc:
            logger.info(f"page during watch raised: {exc!r}")
        terminated_at = _check_old_mongot_terminated(namespace, old_name, old_uid)
        if terminated_at is not None:
            return terminated_at
        time.sleep(0.5)
    return None


def _watch_mongot_termination(namespace: str, old_pod, *, watch_for: float) -> datetime:
    old_name = old_pod.metadata.name
    old_uid = old_pod.metadata.uid
    captured: dict[str, Optional[datetime]] = {"at": None}

    def terminated() -> tuple[bool, str]:
        at = _check_old_mongot_terminated(namespace, old_name, old_uid)
        if at is not None:
            captured["at"] = at
            return True, f"finished_at={at.isoformat()}"
        return False, f"{old_name} still alive (uid={old_uid[:8]})"

    run_periodically(
        terminated,
        timeout=watch_for,
        sleep_time=0.5,
        msg=f"mongot OLD pod {old_name} to terminate",
    )
    at = captured["at"]
    assert at is not None
    return at


def _user_connectivity_tool(namespace: str) -> SearchConnectivityTool:
    return SearchConnectivityTool(rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password))


def _load_mdbs(namespace: str) -> MongoDBSearch:
    # Build from the fixture (fresh run) or load cluster state via try_load —
    # same shape Layer 2 deploys, so the fault tests work even started in isolation.
    helper = SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB.mdb_resource_name,
        mdbs_resource_name=MDBS_NAME,
        ca_configmap_name=MDB.ca_configmap_name,
    )
    return helper.mdbs_for_ext_rs_source(
        MDB.mongot_user_name,
        members=MDB.rs_members,
        lb_mode="Managed",
        clusters=[{"replicas": SEARCH.mongot_replicas}],
    )
