"""Background availability tester for MongoDBSearch.

Daemon thread that drives a ``SearchConnectivityTool`` over an observation
window and accumulates per-iteration ``QueryResult``s. The primary purpose
is to assert that search stays healthy across some external event — use it
as a context manager and read ``tester.verdict`` after the ``with`` block:

    with SearchAvailabilityBackgroundTester(tool) as tester:
        time.sleep(20)
    assert_no_outage(tester.verdict)

Two modes:

* ``paging`` (default) — one page per tick from a long-living cursor;
  reopened on failure or after ``paging_reset_every`` pages.
* ``oneshot`` — one cache-busted ``oneshot_search()`` per tick.
"""

from __future__ import annotations

import logging
import threading
from typing import Optional

from kubetester.mongotester import BackgroundHealthChecker
from tests.common.search.connectivity import ConnectivityVerdict, QueryResult, SearchConnectivityTool

logger = logging.getLogger(__name__)


class SearchAvailabilityBackgroundTester(BackgroundHealthChecker):
    # In paging mode the loop runs as fast as the network allows so that
    # mongod's getMore buffer drains and a real round-trip to mongot
    # surfaces — sleeping would let the cursor be served from cache and
    # hide the fault.
    DEFAULT_WAIT_SEC = 0.0
    DEFAULT_PAGING_RESET_EVERY = 100_000

    def __init__(
        self,
        tool: SearchConnectivityTool,
        mode: str = "paging",
        wait_sec: float = DEFAULT_WAIT_SEC,
        paging_batch_size: int = 100,
        paging_reset_every: int = DEFAULT_PAGING_RESET_EVERY,
    ) -> None:
        if mode not in ("oneshot", "paging"):
            raise ValueError(f"mode must be 'oneshot' or 'paging'; got {mode!r}")
        super().__init__(
            health_function=lambda: None,
            wait_sec=wait_sec,
            allowed_sequential_failures=1,
        )
        self.tool = tool
        self.mode = mode
        self.paging_batch_size = paging_batch_size
        self.paging_reset_every = paging_reset_every
        self._results: list[QueryResult] = []
        self._results_lock = threading.Lock()
        self._cursor = None
        self._cursor_pages_consumed = 0

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
            # wait() with timeout returns immediately on stop() — keeps shutdown fast.
            self._stop_event.wait(self.wait_sec)
        self._close_cursor()

    def stop(self) -> None:
        self._stop_event.set()

    @property
    def verdict(self) -> ConnectivityVerdict:
        with self._results_lock:
            snapshot = list(self._results)
        return self.tool.verdict(snapshot)

    def _run_one_iteration(self) -> QueryResult:
        if self.mode == "oneshot":
            return self.tool.oneshot_search(cache_buster=True)
        return self._read_one_page()

    def _read_one_page(self) -> QueryResult:
        if self._cursor is None or self._cursor_pages_consumed >= self.paging_reset_every:
            self._reopen_cursor()
        pages = self.tool.paging_cursor_read_pages(
            self._cursor,
            pages=1,
            batch_size=self.paging_batch_size,
            first_page_index=self._cursor_pages_consumed,
        )
        page = pages[0]
        self._cursor_pages_consumed += 1
        if not page.success:
            self._close_cursor()
        elif page.returned_count == 0:
            # Cursor exhausted — keep observation continuous by reopening.
            self._close_cursor()
        return page

    def _reopen_cursor(self) -> None:
        self._close_cursor()
        self._cursor = self.tool.paging_cursor_open(batch_size=self.paging_batch_size)
        self._cursor_pages_consumed = 0

    def _close_cursor(self) -> None:
        if self._cursor is None:
            return
        try:
            self._cursor.close()
        except Exception:
            pass
        self._cursor = None
        self._cursor_pages_consumed = 0


def assert_no_outage(verdict: ConnectivityVerdict, min_iterations: int = 5) -> None:
    """Primary assertion: the verdict shows uninterrupted availability.

    Fails if no iterations ran (the harness never executed) or if any
    iteration failed in any class. ``min_iterations`` guards against a
    too-short observation window producing a trivially clean verdict.
    """
    if verdict.total < min_iterations:
        raise AssertionError(
            f"too few iterations to trust verdict: total={verdict.total} < {min_iterations}; "
            f"verdict={verdict.as_dict()}"
        )
    if verdict.failed > 0:
        raise AssertionError(
            f"verdict surfaced failures during a no-outage window: failed={verdict.failed}; "
            f"verdict={verdict.as_dict()}"
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
    if verdict.total == 0:
        raise AssertionError(f"verdict has no iterations — the harness never ran. verdict={verdict.as_dict()}")
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
