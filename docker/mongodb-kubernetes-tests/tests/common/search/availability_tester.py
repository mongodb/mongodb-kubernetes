"""Background availability tester for MongoDBSearch (KUBE-26).

Layered on top of the connectivity tool from KUBE-17, this module drives a
``SearchConnectivityTool`` over a configurable observation window from a
daemon thread and exposes structured per-query results so tests can build
real availability assertions.

The shape mirrors the existing ``MongoDBBackgroundTester`` pattern in
``kubetester/mongotester.py`` — both are thin daemon threads that exercise
a probe and accumulate state — but the search variant captures the full
``QueryResult`` per iteration instead of a binary success/fail count, so
callers can produce a ``ConnectivityVerdict`` at any point during or
after the run and reason about the failure-class taxonomy
(``cursor_lost`` vs ``transient_network`` vs ``other``).

Usage
-----

::

    tool = SearchConnectivityTool(get_rs_search_tester(mdb, ...))
    tester = SearchAvailabilityBackgroundTester(tool, mode="paging")
    tester.start()
    # ... drive a workload, induce a fault, etc. ...
    tester.stop()
    tester.join(timeout=10)
    verdict = tester.get_verdict()
    assert verdict.upstream_alive
    assert verdict.failed == 0  # or, for fault scenarios, > 0

Two demo scenarios live in
``tests/search/search_availability_background_tester.py``:

- **Steady state**: no fault, expect ``upstream_succeeded > 0``,
  ``failed == 0``.
- **Deliberate outage**: mongot pod restart mid-window, expect the
  verdict to surface the failure (``cursor_lost > 0`` or
  ``transient_network > 0``) so the harness is proven to actually
  detect availability loss — without that proof, every downstream
  failure-mode test is a no-op.

No unit tests with mocks — the tester is verified end-to-end against a
real MongoDBSearch deployment via the e2e test above.
"""

from __future__ import annotations

import logging
import threading
import time
from typing import Optional

from kubetester.mongotester import BackgroundHealthChecker
from tests.common.search.connectivity import (
    ConnectivityVerdict,
    QueryResult,
    SearchConnectivityTool,
)

logger = logging.getLogger(__name__)


class SearchAvailabilityBackgroundTester(BackgroundHealthChecker):
    """Daemon-thread harness driving a ``SearchConnectivityTool``.

    Captures every iteration's ``QueryResult`` so tests can produce a
    ``ConnectivityVerdict`` snapshot at any moment via ``get_verdict()``.

    Modes
    -----
    - ``oneshot``: each iteration runs one ``oneshot_search()`` call
      with cache-busting on by default. Suitable for "is upstream
      reachable right now?" probes.
    - ``paging``: each iteration reads one page from a long-living
      paging cursor. The cursor is rotated periodically (every
      ``paging_reset_every`` iterations) to avoid running out of pages
      on small fixtures, and is reopened automatically after any
      failure (including ``cursor_lost``) so the tester continues
      probing the cluster instead of getting stuck on a dead cursor.

    Lifecycle
    ---------
    Starts a daemon thread on ``.start()``; runs until ``.stop()`` is
    called or the test process exits. Tests should call
    ``.stop()`` + ``.join(timeout=N)`` to be sure the last iteration
    has flushed before reading ``get_verdict()``.
    """

    # Default polling interval between iterations (seconds). Lower than
    # the MongoDBBackgroundTester default (3s) because $search queries
    # are an order of magnitude cheaper than the connectivity check
    # MongoDBBackgroundTester drives.
    DEFAULT_WAIT_SEC = 1.0
    # When paging, reopen the cursor after this many iterations even if
    # it hasn't errored, so a long observation window doesn't exhaust
    # the underlying result set.
    DEFAULT_PAGING_RESET_EVERY = 50

    def __init__(
        self,
        tool: SearchConnectivityTool,
        mode: str = "paging",
        wait_sec: float = DEFAULT_WAIT_SEC,
        paging_batch_size: int = 10,
        paging_reset_every: int = DEFAULT_PAGING_RESET_EVERY,
        oneshot_cache_buster: bool = True,
    ) -> None:
        if mode not in ("oneshot", "paging"):
            raise ValueError(f"mode must be 'oneshot' or 'paging'; got {mode!r}")
        # Provide a no-op health_function to BackgroundHealthChecker; we
        # override .run() below so its stock per-iteration call path is
        # never used.
        super().__init__(
            health_function=lambda: None,
            wait_sec=wait_sec,
            allowed_sequential_failures=1,
        )
        self.tool = tool
        self.mode = mode
        self.paging_batch_size = paging_batch_size
        self.paging_reset_every = paging_reset_every
        self.oneshot_cache_buster = oneshot_cache_buster
        self._results: list[QueryResult] = []
        self._results_lock = threading.Lock()
        self._cursor = None
        self._cursor_pages_consumed = 0

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------

    def run(self) -> None:  # noqa: D401 — overrides threading.Thread
        """Daemon-thread loop. Runs one probe per ``wait_sec``."""
        consecutive_failure = 0
        while not self._stop_event.is_set():
            self.number_of_runs += 1
            try:
                result = self._run_one_iteration()
            except Exception as exc:  # pragma: no cover — defensive
                logger.exception(f"{type(self).__name__} iteration crashed: {exc}")
                self.last_exception = exc
                self.exception_number += 1
                consecutive_failure += 1
                self.max_consecutive_failure = max(self.max_consecutive_failure, consecutive_failure)
            else:
                with self._results_lock:
                    self._results.append(result)
                if result.success:
                    consecutive_failure = 0
                else:
                    consecutive_failure += 1
                    self.max_consecutive_failure = max(self.max_consecutive_failure, consecutive_failure)
                    self.exception_number += 1
                    self.last_exception = f"{result.error_class}: {result.error_message}"
            # Sleep in small slices so .stop() takes effect promptly even
            # with a long wait_sec.
            slept = 0.0
            slice_sec = min(0.25, self.wait_sec)
            while slept < self.wait_sec and not self._stop_event.is_set():
                time.sleep(slice_sec)
                slept += slice_sec
        # Final cursor cleanup on exit.
        self._close_cursor()

    def stop(self) -> None:
        """Signal the daemon thread to exit at the next iteration boundary."""
        self._stop_event.set()

    # ------------------------------------------------------------------
    # Iteration body
    # ------------------------------------------------------------------

    def _run_one_iteration(self) -> QueryResult:
        if self.mode == "oneshot":
            return self.tool.oneshot_search(cache_buster=self.oneshot_cache_buster)
        return self._paging_iteration()

    def _paging_iteration(self) -> QueryResult:
        """Read one page from the long-living paging cursor.

        Reopens the cursor if it's missing, exhausted, or hit a
        non-retryable failure on the previous iteration. Avoids the
        otherwise-typical pattern of "cursor died → all subsequent
        iterations see the same failure" — once we've recorded a
        cursor_lost, we want the tester to keep probing the cluster on
        a fresh cursor rather than spamming the same dead-cursor error.
        """
        if self._cursor is None or self._cursor_pages_consumed >= self.paging_reset_every:
            self._reopen_cursor()
        # Defensive: paging_cursor_read_pages handles the per-page
        # mechanics and returns exactly one result per requested page.
        pages = self.tool.paging_cursor_read_pages(
            self._cursor,
            pages=1,
            batch_size=self.paging_batch_size,
            first_page_index=self._cursor_pages_consumed,
            retry_transient_once=True,
        )
        page = pages[0]
        self._cursor_pages_consumed += 1
        # If the page failed, drop the cursor so the next iteration
        # opens a fresh one. This matters most for cursor_lost (the
        # cursor's server-side state is gone — never coming back) but
        # also for transient_network errors that survived the internal
        # retry, where a fresh cursor at least gives the next iteration
        # a clean shot. ``other_failed`` we treat the same way since
        # it usually indicates a query/programming issue.
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
        except Exception:  # pragma: no cover — defensive
            pass
        self._cursor = None
        self._cursor_pages_consumed = 0

    # ------------------------------------------------------------------
    # Snapshots and assertions
    # ------------------------------------------------------------------

    def get_verdict(self) -> ConnectivityVerdict:
        """Snapshot the current verdict over all results captured so far."""
        with self._results_lock:
            snapshot = list(self._results)
        return self.tool.verdict(snapshot)

    def get_results(self) -> list[QueryResult]:
        """Snapshot the per-iteration results captured so far."""
        with self._results_lock:
            return list(self._results)

    def assert_steady_state(
        self,
        min_iterations: int = 5,
        require_upstream_succeeded: bool = True,
        max_failed: int = 0,
    ) -> ConnectivityVerdict:
        """Assert the run looked like a healthy steady state.

        Use after a fault-free observation window. Returns the verdict
        on success so the caller can log it, or raises ``AssertionError``
        with a verdict-shaped message on failure.
        """
        verdict = self.get_verdict()
        if verdict.total < min_iterations:
            raise AssertionError(
                f"steady-state verdict has too few iterations: {verdict.total} < {min_iterations}; "
                f"verdict={verdict.as_dict()}"
            )
        if require_upstream_succeeded and not verdict.upstream_alive:
            raise AssertionError(
                f"steady-state verdict reports no upstream-confirmed pages — the cache-detection "
                f"heuristic may be broken or upstream is silently down. verdict={verdict.as_dict()}"
            )
        if verdict.failed > max_failed:
            raise AssertionError(
                f"steady-state verdict has {verdict.failed} failures (allowed: {max_failed}); "
                f"verdict={verdict.as_dict()}"
            )
        return verdict

    def assert_outage_detected(
        self,
        require_failure: bool = True,
        accept_classes: Optional[tuple[str, ...]] = None,
    ) -> ConnectivityVerdict:
        """Assert the tester observed an outage — KUBE-26 deliverable signal.

        Used in the deliberately-broken-cluster scenario. The tester
        passes the smell test only if it actually catches the outage —
        otherwise downstream failure-mode tests built on it are no-ops.

        Default acceptance set is ``("cursor_lost", "transient_network")``
        because either is a valid signal that upstream availability is
        gone. Tests that want a specific class can narrow the tuple.
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
                f"the background tester missed the fault and would silently report "
                f"availability-loss as healthy. verdict={verdict.as_dict()}"
            )
        return verdict
