"""E2E tests for the search connectivity tool against a single-cluster RS."""

from __future__ import annotations

import time
from datetime import datetime, timezone
from typing import Optional

import pytest
from kubernetes import client
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.bootstrap_test_mixins import (
    MongoDBRsDeploymentConfig,
    MongoDBRsDeploymentTests,
    SearchDeploymentTests,
    SearchE2EFixtures,
    SearchSampleDataAndIndexTests,
    _derive_user_defaults, InstallOperatorTests,
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
from tests.common.search.rs_search_helper import get_rs_search_tester, get_rs_search_tester_for_member

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_connectivity_tool


def configure_mongodb_rs_config(cfg: MongoDBRsDeploymentConfig) -> MongoDBRsDeploymentConfig:
    cfg.mdb_resource_name = "mdb-rs-conn-tool"
    cfg.admin_user_name = ""
    cfg.admin_user_password = ""
    cfg.user_name = ""
    cfg.user_password = ""
    _derive_user_defaults(cfg)
    return cfg


class TestInstallOperator(InstallOperatorTests):
    pass


class TestSearchWithReplicaSet(
    SearchDeploymentTests,
    MongoDBRsDeploymentTests,
):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return configure_mongodb_rs_config(super().build_mongodb_rs_config())


class TestSearchSampleDataAndIndex(
    SearchSampleDataAndIndexTests,
    SearchE2EFixtures,
):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return configure_mongodb_rs_config(super().build_mongodb_rs_config())


class TestSearchConnectivityTool(SearchE2EFixtures):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return configure_mongodb_rs_config(super().build_mongodb_rs_config())

    def test_oneshot_search_succeeds(self, mdb: MongoDB):
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        result = tool.oneshot_search()
        logger.info(f"oneshot_search result: {result}")
        assert result.success, f"one-shot search failed: {result.error_class} {result.error_message}"
        assert result.returned_count > 0, "expected results from cache-busted compound query"

    def test_paging_search_first_page_succeeds(self, mdb: MongoDB):
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        pages = tool.paging_search(pages=3, interval_seconds=0.1, batch_size=20)
        logger.info("paging_search results: %s", "; ".join(str(p) for p in pages))
        assert pages, "paging_search returned no pages"
        assert pages[0].success, f"first page failed: {pages[0].error_class} {pages[0].error_message}"
        assert pages[0].returned_count > 0, "first page returned 0 docs"

    def test_paging_through_mongot_outage_surfaces_connectivity_error(self, mdb: MongoDB, mdbs: MongoDBSearch):
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        pre_pages = tool.paging_search(pages=2, interval_seconds=0.1, batch_size=10)
        logger.info("pre-outage pages: %s", "; ".join(str(p) for p in pre_pages))
        assert any(p.success for p in pre_pages), f"no pre-outage successful page: {pre_pages}"

        statefulset_name = search_resource_names.mongot_statefulset_name(mdbs.name)
        logger.info(f"setting MongoDBSearch {mdbs.name} spec.replicas -> 0 via operator")
        mdbs["spec"]["replicas"] = 0
        mdbs.update()
        wait_for_mongot_statefulset_drained(statefulset_name, mdb.namespace)

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

        logger.info(f"setting MongoDBSearch {mdbs.name} spec.replicas -> 1 via operator")
        mdbs["spec"]["replicas"] = 1
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=200)

    def test_paging_through_mongot_pod_restart_surfaces_lost_cursor(self, mdb: MongoDB, mdbs: MongoDBSearch):
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)
        mongot_selector = f"app={search_resource_names.mongot_statefulset_name(mdbs.name)}-svc"

        _wait_for_search_serving(tool, timeout=200.0)

        def fault() -> None:
            uids = delete_pods(mdb.namespace, label_selector=mongot_selector, grace_period_seconds=0)
            assert (
                len(uids) == 1
            ), f"expected exactly 1 mongot pod matching {mongot_selector}; got {len(uids)}: {list(uids)}"
            _wait_for_search_serving(tool, timeout=200.0)

        _, _, verdict = paging_baseline_and_fault(tool, fault_fn=fault)
        assert verdict.cursor_lost > 0, f"no cursor_lost failure surfaced after mongot pod restart: {verdict.as_dict()}"

    def test_mongot_graceful_shutdown_with_active_cursor_holds_to_grace_and_cursor_dies_with_kill(
        self, mdb: MongoDB, mdbs: MongoDBSearch
    ):
        """SIGTERM with an actively-paged cursor is held until the kubelet
        grace boundary; cursor cannot survive the pod replacement.
        """
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)
        mongot_selector = f"app={search_resource_names.mongot_statefulset_name(mdbs.name)}-svc"

        _wait_for_search_serving(tool)
        old_pod = _single_mongot_pod(mdb.namespace, mongot_selector)
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
            captured_uids = delete_pods(mdb.namespace, label_selector=mongot_selector)

            t_old_exit = _page_until_mongot_terminates(
                tool=tool,
                cursor=cursor,
                namespace=mdb.namespace,
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

            wait_for_all_pods_replaced(mdb.namespace, captured_uids, timeout=180)
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

    def test_mongot_graceful_shutdown_exits_early_after_killcursors(self, mdb: MongoDB, mdbs: MongoDBSearch):
        """killCursors during grace lets mongot exit before grace expires."""
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)
        mongot_selector = f"app={search_resource_names.mongot_statefulset_name(mdbs.name)}-svc"

        _wait_for_search_serving(tool)
        old_pod = _single_mongot_pod(mdb.namespace, mongot_selector)
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
            logger.info(f"deleting mongot pods {mongot_selector} (t_delete={t_delete.isoformat()})")
            captured_uids = delete_pods(mdb.namespace, label_selector=mongot_selector)

            logger.info(f"paging in-grace for 10s while OLD mongot is shutting down (grace={grace}s)")
            _page_for(tool, cursor, duration=10.0, sleep_between=0.5, start_index=2)
        finally:
            t_kill_cursors = datetime.now(timezone.utc)
            logger.info(f"closing cursor — pymongo issues killCursors (t_kill_cursors={t_kill_cursors.isoformat()})")
            try:
                cursor.close()
            except Exception as exc:
                logger.info(f"cursor.close() raised (ignored): {exc!r}")

        t_old_exit = _watch_mongot_termination(mdb.namespace, old_pod, watch_for=grace + 30)
        logger.info(f"OLD pod terminated at {t_old_exit.isoformat()}")
        wait_for_all_pods_replaced(mdb.namespace, captured_uids, timeout=120)

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

    def test_paging_through_envoy_restart_surfaces_lost_cursor(self, mdb: MongoDB, mdbs: MongoDBSearch, namespace: str):
        """Hard-kill envoy mid-cursor; getMore after envoy recovers must surface cursor_lost.

        Envoy pins each mongod→mongot HTTP/2 stream to a specific upstream
        connection. The old envoy pod takes that pinning with it; the
        replacement envoy opens a fresh connection that mongot doesn't
        recognize as the cursor's owner → RST_STREAM → cursor_lost.
        """
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)
        envoy_selector = f"app={search_resource_names.lb_deployment_name(mdbs.name)}"
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

    def test_direct_secondary_paging_succeeds(self, mdb: MongoDB):
        cfg = self.build_mongodb_rs_config()
        ready_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        _wait_for_search_serving(SearchConnectivityTool(ready_tester))

        tester = get_rs_search_tester_for_member(
            mdb,
            member_index=1,
            username=cfg.user_name,
            password=cfg.user_password,
            use_ssl=True,
        )
        tool = SearchConnectivityTool(tester)

        pages = tool.paging_search(pages=5, interval_seconds=0.1, batch_size=10)
        logger.info("secondary pages: %s", "; ".join(str(p) for p in pages))
        assert pages, "no pages returned"
        assert all(
            p.success for p in pages
        ), f"secondary page failures: {[(p.page_index, p.error_class) for p in pages if not p.success]}"
        assert any(p.returned_count > 0 for p in pages), "no page returned docs from secondary"

    def test_direct_secondary_concurrent_with_primary_paging(self, mdb: MongoDB):
        cfg = self.build_mongodb_rs_config()
        primary_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        secondary_tester = get_rs_search_tester_for_member(
            mdb,
            member_index=1,
            username=cfg.user_name,
            password=cfg.user_password,
            use_ssl=True,
        )
        primary_tool = SearchConnectivityTool(primary_tester)
        secondary_tool = SearchConnectivityTool(secondary_tester)

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
