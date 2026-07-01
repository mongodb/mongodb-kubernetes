"""Background availability tester for MongoDBSearch.

Daemon thread that drives a ``SearchConnectivityTool`` over an observation
window and accumulates per-iteration ``QueryResult``s. The primary purpose
is to assert that search stays healthy across some external event — use it
as a context manager and read ``tester.verdict`` after the ``with`` block:

    with SearchAvailabilityBackgroundTester(tool) as tester:
        perform_operations()
    assert_no_outage(tester.verdict)

Two modes:

* ``paging`` (default) — one page per tick from a long-living cursor;
  reopened on failure, after ``paging_reset_every`` pages, or after
  ``paging_reset_after_seconds`` seconds (whichever trips first).
* ``oneshot`` — one cache-busted ``oneshot_search()`` per tick.
"""

from __future__ import annotations

import logging
import threading
import time
from typing import Callable, Optional

import pymongo.errors
from tests.common.search.connectivity import (
    ConnectivityVerdict,
    QueryResult,
    SearchConnectivityTool,
    aggregate_verdicts,
    classify_failure,
)

logger = logging.getLogger(__name__)


class SearchAvailabilityBackgroundTester(threading.Thread):
    DEFAULT_PAGING_RESET_EVERY = 100_000

    def __init__(
        self,
        tool: SearchConnectivityTool,
        mode: str = "paging",
        paging_batch_size: int = 5,
        paging_reset_every: Optional[int] = None,
        paging_reset_after_seconds: Optional[float] = None,
        interval_seconds: float = 0.0,
        query_timeout_ms: Optional[int] = 15_000,
    ) -> None:
        super().__init__()
        self.daemon = True
        if mode not in ("oneshot", "paging"):
            raise ValueError(f"mode must be 'oneshot' or 'paging'; got {mode!r}")
        self.number_of_runs = 0
        self.exception_number = 0
        self.last_exception: Optional[str] = None
        self.max_consecutive_failure = 0
        self._stop_event = threading.Event()
        # Set = paused: the run loop idles without issuing operations, holding the
        # paging cursor open mid-stream so mongod doesn't prefetch the remainder.
        self._pause_event = threading.Event()
        self.tool = tool
        self.mode = mode
        self.paging_batch_size = paging_batch_size
        self.paging_reset_every = paging_reset_every
        # Time-based cursor rollover: close+reopen the paging cursor once it has
        # been open this long, exercising a fresh $search (which needs a live
        # mongot) at a steady cadence through a disruption window.
        self.paging_reset_after_seconds = paging_reset_after_seconds
        # Per-iteration sleep; mutable mid-run to throttle paging (e.g. raise it
        # before a fault so the cursor doesn't drain mongod's buffer faster than
        # the pod terminates).
        self.interval_seconds = interval_seconds
        # Bound each $search so a wedged shard is counted as a failed probe, not a hang.
        self.query_timeout_ms = query_timeout_ms
        self._results: list[QueryResult] = []
        self._results_lock = threading.Lock()
        self._cursor = None
        self._cursor_pages_consumed = 0
        self._cursor_opened_at: Optional[float] = None
        self._cursor_reopens = 0
        self._cursor_ever_opened = False
        self._current_cursor_records = 0

    def __enter__(self) -> "SearchAvailabilityBackgroundTester":
        self.start()
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.stop()
        self.join(timeout=10)
        if self.is_alive():
            logger.warning("background tester thread did not exit within 10s")

    def run(self) -> None:
        consecutive_failure = 0
        while not self._stop_event.is_set():
            # Paused: idle without touching the cursor, staying responsive to stop().
            if self._pause_event.is_set():
                self._stop_event.wait(0.1)
                continue
            self.number_of_runs += 1
            result = self._run_one_iteration()
            with self._results_lock:
                self._results.append(result)
            if result.success:
                consecutive_failure = 0
            else:
                consecutive_failure += 1
                self.max_consecutive_failure = max(self.max_consecutive_failure, consecutive_failure)
                self.exception_number += 1
                self.last_exception = f"{result.error_class}: {result.error_message}"
            # Read fresh each loop so the interval can be altered mid-run; wait
            # on the stop event so stop() stays responsive.
            if self.interval_seconds > 0:
                self._stop_event.wait(self.interval_seconds)
        self._close_cursor()

    def stop(self) -> None:
        self._stop_event.set()

    def pause(self) -> None:
        """Idle without touching the cursor — holds a warm cursor mid-stream across
        a slow fault so mongod doesn't prefetch the rest and mask the upstream loss."""
        self._pause_event.set()

    def resume(self) -> None:
        """Resume issuing operations after pause()."""
        self._pause_event.clear()

    @property
    def verdict(self) -> ConnectivityVerdict:
        with self._results_lock:
            snapshot = list(self._results)
        v = self.tool.verdict(snapshot)
        v.cursor_reopens = self._cursor_reopens
        v.current_cursor_records = self._current_cursor_records
        return v

    @property
    def succeeded_count(self) -> int:
        """Number of successful operations recorded so far."""
        with self._results_lock:
            return sum(1 for r in self._results if r.success)

    @property
    def operations_count(self) -> int:
        """Number of operations (page reads / one-shots) recorded so far, success or failure."""
        with self._results_lock:
            return len(self._results)

    @property
    def failed_count(self) -> int:
        """Number of failed operations recorded so far."""
        with self._results_lock:
            return sum(1 for r in self._results if not r.success)

    def wait_for_operations(
        self,
        count: int,
        *,
        since: Optional[int] = None,
        timeout: float = 120.0,
        stop_on_fault: bool = False,
    ) -> int:
        """Block until ``count`` more operations (success or failure) are
        recorded beyond ``since`` (defaults to the current count). With
        ``stop_on_fault=True``, returns early on the first new failure. Raises
        on timeout or if the tester thread dies first.
        """
        if since is None:
            since = self.operations_count
        failed_baseline = self.failed_count
        deadline = time.monotonic() + timeout
        target = since + count
        while time.monotonic() < deadline:
            current = self.operations_count
            if stop_on_fault and self.failed_count > failed_baseline:
                return current
            if current >= target:
                return current
            if not self.is_alive():
                raise AssertionError(
                    f"tester thread died before reaching {target} operations "
                    f"(current={current}); last_exception={self.last_exception}"
                )
        raise AssertionError(
            f"tester did not reach {target} operations within {timeout}s; current={self.operations_count}"
        )

    def _run_one_iteration(self) -> QueryResult:
        if self.mode == "oneshot":
            return self.tool.oneshot_search(cache_buster=True, timeout_ms=self.query_timeout_ms)
        return self._read_one_page()

    def _should_reset_cursor(self) -> bool:
        if self._cursor is None:
            return True
        if self.paging_reset_every is not None and self._cursor_pages_consumed >= self.paging_reset_every:
            return True
        if (
            self.paging_reset_after_seconds is not None
            and self._cursor_opened_at is not None
            and (time.monotonic() - self._cursor_opened_at) >= self.paging_reset_after_seconds
        ):
            return True
        return False

    def _read_one_page(self) -> QueryResult:
        if self._should_reset_cursor():
            reopen_failure = self._reopen_cursor()
            if reopen_failure is not None:
                return reopen_failure
        pages = self.tool.paging_cursor_read_pages(
            self._cursor,
            pages=1,
            batch_size=self.paging_batch_size,
            first_page_index=self._cursor_pages_consumed,
        )
        page = pages[0]
        self._cursor_pages_consumed += 1
        self._current_cursor_records += page.returned_count
        if not page.success:
            self._close_cursor()
        elif page.returned_count == 0:
            # Cursor exhausted — keep observation continuous by reopening.
            self._close_cursor()
        return page

    def _reopen_cursor(self) -> Optional[QueryResult]:
        """(Re)open the paging cursor.

        Opening a cursor while the upstream is down (e.g. mongot/envoy
        returning "no healthy upstream") raises a pymongo error. Return it as
        a failure ``QueryResult`` so the run loop records an outage instead of
        the daemon thread dying; ``None`` on success.
        """
        self._close_cursor()
        started = time.monotonic()
        try:
            self._cursor = self.tool.paging_cursor_open(batch_size=self.paging_batch_size)
            self._cursor_pages_consumed = 0
            self._cursor_opened_at = time.monotonic()
            self._current_cursor_records = 0
            if self._cursor_ever_opened:
                self._cursor_reopens += 1
            self._cursor_ever_opened = True
            return None
        except pymongo.errors.PyMongoError as exc:
            elapsed_ms = (time.monotonic() - started) * 1000.0
            klass, code = SearchConnectivityTool._classify_error(exc)
            msg = str(exc)
            return QueryResult(
                success=False,
                latency_ms=elapsed_ms,
                error_class=klass,
                error_code=code,
                error_message=msg,
                failure_class=classify_failure(klass, code, msg),
            )

    def _close_cursor(self) -> None:
        if self._cursor is None:
            return
        try:
            self._cursor.close()
        except Exception:
            pass
        self._cursor = None
        self._cursor_pages_consumed = 0
        self._cursor_opened_at = None


def assert_no_outage(verdict: ConnectivityVerdict, min_operations: int = 5) -> None:
    """Primary assertion: the verdict shows uninterrupted availability.

    Fails if no operations ran (the harness never executed) or if any
    operation failed in any class. ``min_operations`` guards against a
    too-short observation window producing a trivially clean verdict.
    """
    if verdict.total_operations < min_operations:
        raise AssertionError(
            f"too few operations to trust verdict: total_operations={verdict.total_operations} "
            f"< {min_operations}; verdict={verdict.as_dict()}"
        )
    if verdict.failed > 0:
        raise AssertionError(
            f"verdict surfaced failures during a no-outage window: failed={verdict.failed}; "
            f"verdict={verdict.as_dict()}"
        )
    if verdict.total_returned_records == 0:
        raise AssertionError(
            f"no records received across {verdict.total_operations} operations — availability is "
            f"trivially clean (empty cursor reads succeed but return nothing); verdict={verdict.as_dict()}"
        )


def assert_outage_detected(
    verdict: ConnectivityVerdict,
    accept_classes: Optional[tuple[str, ...]] = None,
) -> None:
    """Secondary assertion: at least one failure of an accepted class surfaced.

    Default accepted set is ``("cursor_lost", "transient_network")``.
    Used by fault-injection tests to prove the harness sees the fault.
    """
    accept = accept_classes or ("cursor_lost", "transient_network")
    if verdict.total_operations == 0:
        raise AssertionError(f"verdict has no operations — the harness never ran. verdict={verdict.as_dict()}")
    observed = {
        "cursor_lost": verdict.cursor_lost,
        "transient_network": verdict.transient_network,
        "other": verdict.other_failed,
    }
    if not any(observed[c] > 0 for c in accept):
        raise AssertionError(
            f"verdict did not surface a {' or '.join(accept)} failure — "
            f"the background tester missed the fault. verdict={verdict.as_dict()}"
        )


class PagingAvailabilityFleet:
    """A fleet of independent paging background testers, each owning its own
    connection and paging cursor.

    mongod routes every new ``$search`` to a randomly-chosen mongot replica, so a
    single paging cursor only exercises one replica. Running several concurrent
    cursors covers all mongot replicas with high probability — size the fleet a
    few times the replica count. Presents the same context-manager +
    ``wait_for_operations`` + ``verdict`` surface as a single
    ``SearchAvailabilityBackgroundTester``, so it drops into the same
    ``assert_no_outage`` flow.
    """

    def __init__(
        self,
        tool_factory: Callable[[], SearchConnectivityTool],
        size: int,
        *,
        interval_seconds: float = 0.0,
        paging_batch_size: int = 5,
        paging_reset_every: Optional[int] = None,
        paging_reset_after_seconds: Optional[float] = None,
    ) -> None:
        if size < 1:
            raise ValueError(f"size must be >= 1; got {size}")
        self._testers = [
            SearchAvailabilityBackgroundTester(
                tool_factory(),
                mode="paging",
                interval_seconds=interval_seconds,
                paging_batch_size=paging_batch_size,
                paging_reset_every=paging_reset_every,
                paging_reset_after_seconds=paging_reset_after_seconds,
            )
            for _ in range(size)
        ]

    def __enter__(self) -> "PagingAvailabilityFleet":
        for tester in self._testers:
            tester.start()
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        for tester in self._testers:
            tester.stop()
        for tester in self._testers:
            tester.join(timeout=10)
            if tester.is_alive():
                logger.warning("paging fleet tester did not exit within 10s")

    def wait_for_operations(self, count: int, *, timeout: float = 120.0) -> None:
        """Block until every member records ``count`` more operations than it had
        when this call started."""
        since = [tester.operations_count for tester in self._testers]
        for tester, baseline in zip(self._testers, since):
            tester.wait_for_operations(count, since=baseline, timeout=timeout)

    @property
    def verdict(self) -> ConnectivityVerdict:
        """Fleet-wide verdict — the per-member verdicts summed."""
        return aggregate_verdicts([tester.verdict for tester in self._testers])


class MultiClusterAvailabilityFleet:
    """A oneshot + paging background tester per member cluster, each pinned to that
    cluster via ``tester_factory``. Under a single-cluster fault the faulted cluster must
    surface an outage in both modes while the others keep serving. ``paging_reset_every``
    forces periodic cursor reopens so a sustained outage faults the paging testers
    promptly. Use as a context manager.
    """

    def __init__(
        self,
        tester_factory: Callable[[int], SearchConnectivityTool],
        cluster_indexes: list[int],
        *,
        modes: tuple[str, ...] = ("oneshot", "paging"),
        interval_seconds: float = 0.2,
        paging_batch_size: int = 5,
        paging_reset_every: int = 50000,
    ) -> None:
        if not cluster_indexes:
            raise ValueError("cluster_indexes must be non-empty")
        if not modes:
            raise ValueError("modes must be non-empty")
        self.cluster_indexes = list(cluster_indexes)
        self.modes = tuple(modes)
        # Keyed by (cluster_index, mode); tester_factory is called once per tester so
        # each owns its own pinned connection.
        self._testers: dict[tuple[int, str], SearchAvailabilityBackgroundTester] = {
            (idx, mode): SearchAvailabilityBackgroundTester(
                tester_factory(idx),
                mode=mode,
                interval_seconds=interval_seconds,
                paging_batch_size=paging_batch_size,
                paging_reset_every=(paging_reset_every if mode == "paging" else None),
            )
            for idx in self.cluster_indexes
            for mode in self.modes
        }

    def __enter__(self) -> "MultiClusterAvailabilityFleet":
        for tester in self._testers.values():
            tester.start()
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        for tester in self._testers.values():
            tester.stop()
        for tester in self._testers.values():
            tester.join(timeout=10)
            if tester.is_alive():
                logger.warning("fleet background tester did not exit within 10s")

    def tester(self, cluster_index: int, mode: str) -> SearchAvailabilityBackgroundTester:
        return self._testers[(cluster_index, mode)]

    def verdict(self, cluster_index: int, mode: str) -> ConnectivityVerdict:
        return self._testers[(cluster_index, mode)].verdict

    def wait_for_operations_all(self, count: int, *, timeout: float = 240.0) -> None:
        """Block until every (cluster, mode) tester records ``count`` more operations."""
        since = {key: tester.operations_count for key, tester in self._testers.items()}
        for key, tester in self._testers.items():
            tester.wait_for_operations(count, since=since[key], timeout=timeout)

    def assert_single_cluster_outage(
        self,
        faulted_index: int,
        *,
        oneshot_accept: tuple[str, ...] = ("transient_network",),
        min_survivor_operations: int = 5,
    ) -> None:
        """The faulted cluster surfaces the outage on NEW queries (oneshot mode); every
        other cluster stays available in all modes across the whole window.

        Paging mode on the faulted cluster is deliberately NOT required to fault: a sharded
        (and RS) ``$search`` aggregation cursor is materialized in mongod at establishment —
        mongod eagerly drains mongot's result and closes the mongot stream before the client's
        first getMore — so an already-open paging cursor rides through a mongot outage and
        keeps serving from mongod's buffer. Only a *new* ``$search`` (oneshot, or a paging
        reopen) needs mongot. Requiring the established paging cursor to fault would contradict
        that proven behavior; we log its verdict for visibility instead.
        """
        if "oneshot" not in self.modes:
            raise AssertionError("assert_single_cluster_outage needs a 'oneshot' tester to detect the fault")
        faulted_oneshot = self.verdict(faulted_index, "oneshot")
        logger.info(f"fleet: faulted cluster {faulted_index} [oneshot] verdict={faulted_oneshot.as_dict()}")
        assert_outage_detected(faulted_oneshot, accept_classes=oneshot_accept)
        if "paging" in self.modes:
            faulted_paging = self.verdict(faulted_index, "paging")
            logger.info(
                f"fleet: faulted cluster {faulted_index} [paging] verdict={faulted_paging.as_dict()} "
                f"— established cursor rides through the mongot outage (eager-drain), as expected"
            )
        for idx in self.cluster_indexes:
            if idx == faulted_index:
                continue
            for mode in self.modes:
                survivor_verdict = self.verdict(idx, mode)
                logger.info(f"fleet: survivor cluster {idx} [{mode}] verdict={survivor_verdict.as_dict()}")
                assert_no_outage(survivor_verdict, min_operations=min_survivor_operations)

    def assert_no_outage(self, *, min_operations: int = 5) -> None:
        """Every (cluster, mode) tester stayed available across the whole window — no failed
        op, records returned. Use when no fault (or only a benign op, e.g. a scale up/down)
        was applied to the fleet. (Bare ``assert_no_outage`` below is the module function.)"""
        for idx in self.cluster_indexes:
            for mode in self.modes:
                verdict = self.verdict(idx, mode)
                logger.info(f"fleet: cluster {idx} [{mode}] verdict={verdict.as_dict()}")
                assert_no_outage(verdict, min_operations=min_operations)
