import threading
import time
from collections import deque
from dataclasses import dataclass, field
from typing import Optional

import pymongo.errors
from tests import test_logger
from tests.common.search.search_tester import SearchTester

logger = test_logger.get_test_logger(__name__)


@dataclass
class QPSSnapshot:
    """A point-in-time snapshot of QPS, cumulative page count, and error count."""

    elapsed_s: float
    qps: dict[float, float]  # window_s -> QPS value at this moment
    total_count: int          # cumulative pages recorded so far
    total_errors: int         # cumulative errors recorded so far


class SlidingWindowQPS:
    """Thread-safe sliding window QPS tracker backed by two background threads.

    Thread 1 — sampler (0.25s interval): snapshots the cumulative count into a
    rolling deque so that qps() can compute Δcount/Δtime for any window. QPS
    naturally falls to 0 when no pages complete (e.g. during an outage).

    Thread 2 — history recorder (1s interval by default): calls qps() for every
    configured window and appends a QPSSnapshot to _history, giving a full
    second-by-second record of throughput across the lifetime of the run.
    """

    def __init__(
        self,
        windows: tuple[float, ...] = (1.0, 5.0),
        sample_interval: float = 0.25,
        history_interval: float = 1.0,
    ):
        self._windows = windows
        self._lock = threading.Lock()
        self._cumulative: int = 0
        self._error_count: int = 0
        self._start_time: float = time.monotonic()
        # Rolling deque of (timestamp, cumulative_count) used by qps()
        self._samples: deque[tuple[float, int]] = deque()
        # Full history of QPSSnapshots, one per history_interval tick
        self._history: list[QPSSnapshot] = []
        self._stop = threading.Event()
        for target, interval in (
            (self._sample_loop, sample_interval),
            (self._history_loop, history_interval),
        ):
            threading.Thread(target=target, args=(interval,), daemon=True).start()

    def record(self):
        """Increment the completed-page counter."""
        with self._lock:
            self._cumulative += 1

    def record_error(self):
        """Increment the error counter."""
        with self._lock:
            self._error_count += 1

    def _sample_loop(self, interval: float):
        while not self._stop.wait(interval):
            now = time.monotonic()
            cutoff = now - max(self._windows)
            with self._lock:
                self._samples.append((now, self._cumulative))
                while self._samples and self._samples[0][0] < cutoff:
                    self._samples.popleft()

    def _history_loop(self, interval: float):
        while not self._stop.wait(interval):
            elapsed = time.monotonic() - self._start_time
            # Compute QPS outside the lock (qps() acquires/releases it internally)
            qps_values = {w: self.qps(w) for w in self._windows}
            with self._lock:
                self._history.append(
                    QPSSnapshot(
                        elapsed_s=round(elapsed, 2),
                        qps=qps_values,
                        total_count=self._cumulative,
                        total_errors=self._error_count,
                    )
                )

    def stop(self):
        """Stop both background threads."""
        self._stop.set()

    @property
    def history(self) -> list[QPSSnapshot]:
        with self._lock:
            return list(self._history)

    def qps(self, window_s: float) -> float:
        """Return queries per second over the last window_s seconds.

        Computed as (Δcount) / (Δtime). The current cumulative count is used
        as a synthetic "now" data point so that completions are reflected
        immediately without waiting for the next sampler tick. This also means
        a gap with no completions (e.g. an outage) correctly produces 0.0,
        because the sampled history shows a flat count over the window.
        """
        now = time.monotonic()
        cutoff = now - window_s
        with self._lock:
            in_window = [(t, c) for t, c in self._samples if t >= cutoff]
            in_window.append((now, self._cumulative))
        if len(in_window) < 2:
            return 0.0
        elapsed = in_window[-1][0] - in_window[0][0]
        if elapsed <= 0:
            return 0.0
        return (in_window[-1][1] - in_window[0][1]) / elapsed

    def format(self) -> str:
        """Format current QPS for all configured windows as a human-readable string."""
        parts = [f"{w:.0f}s: {self.qps(w):.2f}" for w in self._windows]
        return "QPS [" + ", ".join(parts) + "]"

    def log_history(self):
        """Log the full QPS history table — one row per history_interval tick."""
        snapshots = self.history
        if not snapshots:
            logger.info("QPS history: no data recorded")
            return
        window_labels = "  ".join(f"{'QPS(' + str(int(w)) + 's)':>10}" for w in self._windows)
        logger.info(f"QPS history:   elapsed  {window_labels}  {'pages':>8}  {'errors':>8}")
        for s in snapshots:
            qps_cols = "  ".join(f"{s.qps[w]:>10.2f}" for w in self._windows)
            error_marker = " !" if s.total_errors > 0 else "  "
            logger.info(f"  t={s.elapsed_s:7.2f}s  {qps_cols}  {s.total_count:>8d}  {s.total_errors:>6d}{error_marker}")


@dataclass
class SinglePagingResult:
    total_docs: int
    total_pages: int


@dataclass
class UserPagingStats:
    user_id: int
    total_queries: int = 0
    total_pages: int = 0
    total_docs: int = 0
    duration_s: float = 0.0
    errors: list[Exception] = field(default_factory=list)

    @property
    def avg_qps(self) -> float:
        return self.total_queries / self.duration_s if self.duration_s > 0 else 0.0


@dataclass
class ConcurrentPagingStats:
    per_user: list[UserPagingStats] = field(default_factory=list)
    duration_s: float = 0.0

    @property
    def total_queries(self) -> int:
        return sum(u.total_queries for u in self.per_user)

    @property
    def total_pages(self) -> int:
        return sum(u.total_pages for u in self.per_user)

    @property
    def total_docs(self) -> int:
        return sum(u.total_docs for u in self.per_user)

    @property
    def all_errors(self) -> list[Exception]:
        return [e for u in self.per_user for e in u.errors]

    @property
    def total_errors(self) -> int:
        return sum(len(u.errors) for u in self.per_user)

    @property
    def avg_qps(self) -> float:
        return self.total_queries / self.duration_s if self.duration_s > 0 else 0.0

    def log_summary(self):
        logger.info(
            f"Concurrent paging summary: "
            f"{len(self.per_user)} users, "
            f"{self.total_queries} queries, "
            f"{self.total_pages} pages, "
            f"{self.total_docs} docs, "
            f"{self.total_errors} errors, "
            f"wall time: {self.duration_s:.2f}s, "
            f"avg QPS: {self.avg_qps:.2f}"
        )
        for u in self.per_user:
            if u.errors:
                status = f"{len(u.errors)} error(s): " + "; ".join(str(e) for e in u.errors)
            else:
                status = "OK"
            logger.info(
                f"  User {u.user_id}: {u.total_queries} queries, "
                f"{u.total_pages} pages, {u.total_docs} docs, "
                f"{u.duration_s:.2f}s, avg QPS: {u.avg_qps:.2f} — {status}"
            )


class SearchPagingQueryHelper:
    """Helper that executes a $search query and pages through results to verify cursor paging (getMore) works.

    Executes a text search returning 100+ documents, then iterates through the cursor
    page by page with a configurable sleep between pages to simulate realistic getMore traffic.

    Each instance should be created with its own SearchTester to avoid sharing connection pools,
    especially when used with run_concurrent_paging_queries.
    """

    def __init__(
        self,
        search_tester: SearchTester,
        db_name: str,
        col_name: str,
        page_size: int = 5,
        page_sleep_ms: int = 100,
    ):
        self.search_tester = search_tester
        self.db_name = db_name
        self.col_name = col_name
        self.page_size = page_size
        self.page_sleep_ms = page_sleep_ms

    def execute_paging_query(self, index: str = "default", silent: bool = False) -> int:
        """Execute a $search query and page through all results in batches.

        Uses a text search on the title field for "night", which returns ~150 documents
        from the sample_mflix.movies collection — reliably above the 100-document minimum.

        Query counts measured on sample_mflix.movies (21,349 total documents):
          text title "night"            ~150
          text title "dead"             ~105
          text title "war"               ~91
          text title "life"             ~201
          text title "man"              ~292
          text title "love"             ~304
          text plot  "police"           ~396
          text plot  "murder"           ~415
          text plot  "action adventure" ~237
          text plot  "love story"      ~2541
          wildcard   "*"              ~21348

        Args:
            index: Name of the search index to use.
            silent: If True, suppress per-page logs; only print a summary line at the end.

        Returns:
            Total number of documents fetched across all pages.
        """
        result = self._execute_paging_query_internal(index=index, silent=silent)
        return result.total_docs

    def _execute_paging_query_internal(
        self,
        index: str = "default",
        silent: bool = False,
        qps_tracker: Optional["SlidingWindowQPS"] = None,
    ) -> SinglePagingResult:
        collection = self.search_tester.client[self.db_name][self.col_name]
        pipeline = [
            {"$search": {"index": index, "text": {"query": "night", "path": "title"}}},
            {"$project": {"_id": 0, "title": 1, "score": {"$meta": "searchScore"}}},
        ]

        cursor = collection.aggregate(pipeline, batchSize=self.page_size)

        page_num = 0
        total_fetched = 0
        page = []

        for doc in cursor:
            page.append(doc)
            if len(page) == self.page_size:
                page_num += 1
                total_fetched += len(page)
                if qps_tracker is not None:
                    qps_tracker.record()
                if not silent:
                    logger.info(
                        f"Page {page_num}: fetched {len(page)} documents, total so far: {total_fetched}"
                    )
                page = []
                time.sleep(self.page_sleep_ms / 1000)

        if page:
            page_num += 1
            total_fetched += len(page)
            if qps_tracker is not None:
                qps_tracker.record()
            if not silent:
                logger.info(
                    f"Page {page_num} (last): fetched {len(page)} documents, total so far: {total_fetched}"
                )

        logger.info(
            f"Paging complete: {page_num} pages, {total_fetched} total documents "
            f"(page_size={self.page_size}, sleep={self.page_sleep_ms}ms)"
        )
        return SinglePagingResult(total_docs=total_fetched, total_pages=page_num)

    def _run_iterations(
        self,
        user_id: int,
        iterations: int,
        silent: bool,
        ignore_errors: bool,
        stop_event: threading.Event,
        stats: UserPagingStats,
        qps_tracker: SlidingWindowQPS,
    ):
        """Worker target: run the paging query for the given number of iterations.

        Stops early if stop_event is set (e.g. another user hit an error).
        When ignore_errors=False, any error sets stop_event and halts all users.
        When ignore_errors=True, errors are logged and counted but iteration continues.
        """
        start = time.monotonic()
        i = 0
        try:
            while (iterations < 0 or i < iterations) and not stop_event.is_set():
                try:
                    result = self._execute_paging_query_internal(silent=silent, qps_tracker=qps_tracker)
                    stats.total_queries += 1
                    stats.total_pages += result.total_pages
                    stats.total_docs += result.total_docs
                    if silent:
                        logger.info(
                            f"User {user_id}: run {stats.total_queries} done — "
                            f"{result.total_pages} pages, {result.total_docs} docs | "
                            f"{qps_tracker.format()}"
                        )
                except Exception as e:
                    stats.errors.append(e)
                    qps_tracker.record_error()
                    logger.error(f"User {user_id}: error (total errors: {len(stats.errors)}) — {e}")
                    if not ignore_errors:
                        stop_event.set()
                        return
                i += 1
        finally:
            stats.duration_s = time.monotonic() - start


def run_concurrent_paging_queries(
    helpers: list[SearchPagingQueryHelper],
    iterations: int = 1,
    silent: bool = False,
    ignore_errors: bool = True,
) -> ConcurrentPagingStats:
    """Run paging queries concurrently across multiple helpers to simulate multiple users.

    Each helper must be created with its own SearchTester instance so that each simulated
    user has an independent connection pool to the database.

    Args:
        helpers: One SearchPagingQueryHelper per simulated user, each with its own SearchTester.
        iterations: Number of full paging runs per user. Pass -1 for unlimited (runs until error).
        silent: If True, suppress per-page logs and only print a summary line per completed run
                that includes sliding window page-level QPS (1s and 5s windows).
                QPS counts individual page fetches (getMore operations), not full paging runs.
        ignore_errors: If True (default), log errors and continue running instead of stopping
                       all users. Error counts and messages are tracked in stats and QPS history.
                       Set to False to halt all users on the first error and raise RuntimeError.

    Returns:
        ConcurrentPagingStats aggregating results across all users.

    Raises:
        RuntimeError: If any errors occurred and ignore_errors=False (after all threads stop).
                      With ignore_errors=True (default) errors are surfaced in stats only.
    """
    stop_event = threading.Event()
    per_user_stats = [UserPagingStats(user_id=i) for i in range(len(helpers))]
    qps_tracker = SlidingWindowQPS(windows=(1.0, 5.0))

    threads = [
        threading.Thread(
            target=helper._run_iterations,
            args=(i, iterations, silent, ignore_errors, stop_event, per_user_stats[i], qps_tracker),
            daemon=True,
        )
        for i, helper in enumerate(helpers)
    ]

    wall_start = time.monotonic()
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    wall_duration = time.monotonic() - wall_start
    qps_tracker.stop()

    stats = ConcurrentPagingStats(per_user=per_user_stats, duration_s=wall_duration)
    stats.log_summary()
    qps_tracker.log_history()

    if stats.all_errors and not ignore_errors:
        raise RuntimeError(
            f"{stats.total_errors} error(s) encountered: {stats.all_errors}"
        )

    return stats
