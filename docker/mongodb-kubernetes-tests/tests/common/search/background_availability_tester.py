"""Background availability tester for MongoDBSearch.

Daemon-thread harness that drives a ``SearchConnectivityTool`` over an
observation window and accumulates per-iteration ``QueryResult``s so tests
can produce a ``ConnectivityVerdict`` snapshot at any time.

Two modes:
- ``oneshot`` — one cache-busted ``oneshot_search()`` per tick.
- ``paging`` — one page per tick from a long-living cursor; reopened on
  failure or after ``paging_reset_every`` iterations.
"""

from __future__ import annotations

import logging
import threading
import time
from typing import Optional

from kubetester.mongotester import BackgroundHealthChecker
from tests.common.search.connectivity import ConnectivityVerdict, QueryResult, SearchConnectivityTool

logger = logging.getLogger(__name__)


class SearchAvailabilityBackgroundTester(BackgroundHealthChecker):
    """Daemon-thread harness driving a ``SearchConnectivityTool``."""

    DEFAULT_WAIT_SEC = 1.0
    DEFAULT_PAGING_RESET_EVERY = 50

    def __init__(
        self,
        tool: SearchConnectivityTool,
        mode: str = "paging",
        wait_sec: float = DEFAULT_WAIT_SEC,
        paging_batch_size: int = 10,
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
            self._sleep_responsively()
        self._close_cursor()

    def _sleep_responsively(self) -> None:
        """Sleep in 0.25s slices so .stop() takes effect within one slice."""
        slept = 0.0
        slice_sec = min(0.25, self.wait_sec)
        while slept < self.wait_sec and not self._stop_event.is_set():
            time.sleep(slice_sec)
            slept += slice_sec

    def stop(self) -> None:
        self._stop_event.set()

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

    def get_verdict(self) -> ConnectivityVerdict:
        with self._results_lock:
            snapshot = list(self._results)
        return self.tool.verdict(snapshot)

    def assert_outage_detected(
        self,
        require_failure: bool = True,
        accept_classes: Optional[tuple[str, ...]] = None,
    ) -> ConnectivityVerdict:
        """Assert at least one failure of the accepted classes surfaced.

        Default acceptance set is ``("cursor_lost", "transient_network")``.
        """
        verdict = self.get_verdict()
        if not require_failure:
            return verdict
        accept = accept_classes or ("cursor_lost", "transient_network")
        observed = {
            "cursor_lost": verdict.cursor_lost,
            "transient_network": verdict.transient_network,
            "other": verdict.other_failed,
        }
        if not any(observed[c] > 0 for c in accept):
            raise AssertionError(
                f"outage-scenario verdict did not surface a {' or '.join(accept)} failure — "
                f"the background tester missed the fault. verdict={verdict.as_dict()}"
            )
        return verdict
