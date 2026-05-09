"""Search connectivity tool for MongoDBSearch availability testing.

Reusable Python module that issues `$search` and `$vectorSearch` queries against
an MCK-deployed MongoDB cluster and returns structured per-query results so
callers can build availability and load-balancer-correctness tests on top of
real query traffic.

Modes
-----

- **One-shot**: a single `$search` query and/or a single `$vectorSearch` query.
  Largely formalises the API that already lives in
  ``movies_search_helper.py`` so other tests/modules can call into it cleanly.
- **Long-running paging**: open a single `$search` aggregation cursor and page
  through it for a configurable number of pages, with a configurable interval
  between pages.

Cache-distinguishing strategy
-----------------------------

mongod and mongot cache aggressively. A naïve "did the query succeed?" check
can stay green long after upstream (envoy/mongot) has gone away — pymongo's
local cursor buffer keeps draining, mongot's per-query cache keeps returning
the same answer. This module exposes a per-query ``cache_hit_hint`` that
combines two signals:

1. **Buffer-vs-getMore detection (paging mode).** Before pulling each document
   from the server cursor we inspect the pymongo CommandCursor's local buffer
   length. If the buffer is empty, the upcoming ``next()`` will issue a
   ``getMore`` against the server — which means the page actually contacts
   mongot/envoy. If the buffer is non-empty for the whole page, the page was
   served entirely from already-fetched batch state and tells us **nothing**
   about upstream availability.
2. **Cache-buster query (one-shot mode).** Each one-shot search may inject a
   unique random token into a ``compound.should`` clause so mongot cannot
   short-circuit the result from its query cache — the query identity is
   different on every call. The accompanying latency band (configurable via
   ``cache_latency_threshold_ms``) provides a secondary heuristic in case the
   sentinel-injection path is not used.

For the strongest guarantee that "this query reached upstream", callers should
use one-shot mode with cache-busting *and* assert
``cache_hit_hint is False``. Paging-mode results expose the same flag per
page, so a long-running tester that wants a high-confidence verdict can simply
filter to pages where ``cache_hit_hint is False`` and assert at least one such
page succeeded over the observation window.

No unit tests with mocks — verified end-to-end on real systems via the e2e
tests under ``tests/search/``.
"""

from __future__ import annotations

import os
import re
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, Callable, Optional

import pymongo
import pymongo.errors
import requests
from tests import test_logger
from tests.common.search.search_tester import SearchTester

logger = test_logger.get_test_logger(__name__)

# Voyage embedding endpoint configuration is mirrored from movies_search_helper.py
# so callers don't have to dance between modules. The env var name matches the
# project convention; tests should gate on its presence with pytest.skipif.
EMBEDDING_QUERY_KEY_ENV_VAR = "AI_MONGODB_EMBEDDING_QUERY_KEY"
VOYAGE_EMBEDDING_ENDPOINT = "https://ai.mongodb.com/v1/embeddings"
VOYAGE_MODEL = "voyage-3-large"
VOYAGE_DIMENSIONS = 2048


# ----------------------------------------------------------------------
# Failure-class taxonomy
# ----------------------------------------------------------------------
#
# Tests that exercise upstream availability need to distinguish between
# failures that mean "the server has lost the cursor's state" (a hard,
# non-retryable signal that the long-living cursor is dead) and failures
# that mean "we couldn't reach the server right now" (a soft, often-
# retryable signal that doesn't on its own tell us anything about the
# cursor's state). Three buckets:
#
#   FAILURE_CURSOR_LOST       — the cursor's server-side state is gone
#                                and retrying on the same cursor cannot
#                                help. Three signal patterns:
#                                  (a) pymongo's CursorNotFound (server
#                                      error code 43) — the canonical
#                                      server-side cursor-killed signal
#                                      (TTL expiry, killCursors, etc.).
#                                  (b) OperationFailure messages
#                                      matching "cursor id N not found"
#                                      / "cursor id N was killed" —
#                                      same condition surfaced by some
#                                      pymongo paths via the parent
#                                      class instead of CursorNotFound.
#                                  (c) For $search cursors specifically:
#                                      mongod returns InternalError
#                                      (code 1) wrapping a mongot-side
#                                      "Remote error from mongot" or
#                                      "RST_STREAM" message when the
#                                      gRPC stream between mongod and
#                                      mongot is torn down (e.g. mongot
#                                      pod restart). The cursor's
#                                      mongot-side session is gone; the
#                                      cursor is dead even though the
#                                      surface error code isn't 43.
#                                NEVER retried.
#   FAILURE_TRANSIENT_NETWORK — we couldn't talk to the server: a hard
#                                pymongo network class (NetworkTimeout,
#                                AutoReconnect, ConnectionFailure,
#                                ServerSelectionTimeoutError) OR an
#                                OperationFailure whose message says the
#                                LB returned 503 / "no healthy upstream"
#                                / "connection refused" (envoy's response
#                                when no mongot is healthy). Retried
#                                ONCE internally before being recorded.
#   FAILURE_OTHER             — anything else (genuine server-side query
#                                errors, schema problems, etc). Recorded
#                                as-is; never retried.
FAILURE_CURSOR_LOST = "cursor_lost"
FAILURE_TRANSIENT_NETWORK = "transient_network"
FAILURE_OTHER = "other"

_CURSOR_LOST_MESSAGE_RE = re.compile(
    r"cursor id .*?(not found|was killed)|"
    r"remote error from mongot|"
    r"rst_stream",
    re.IGNORECASE,
)
_TRANSIENT_NETWORK_MESSAGE_RE = re.compile(
    r"no healthy upstream|connection refused|connection reset|broken pipe",
    re.IGNORECASE,
)
_TRANSIENT_NETWORK_CLASSES = frozenset(
    {
        "NetworkTimeout",
        "AutoReconnect",
        "ConnectionFailure",
        "ServerSelectionTimeoutError",
    }
)


def classify_failure(error_class: str, error_code: Optional[int], error_message: str) -> str:
    """Map a pymongo-derived ``(class, code, message)`` triple to one of the
    three failure buckets above.

    Pure function on three primitives so callers (and unit tests) don't have
    to reconstruct the original exception object. The original exception is
    already classified into ``error_class`` / ``error_code`` /
    ``error_message`` by ``SearchConnectivityTool._classify_error`` upstream.

    Cursor-lost takes precedence over transient_network when both might
    match. Concretely, a "Remote error from mongot" surfacing during a
    transient mongot restart is a cursor-lost: mongot has lost the
    cursor's stream-side state and won't recover it, even though mongot
    itself comes back. Treating it as transient_network would incorrectly
    suggest that retrying the same cursor would succeed.
    """
    # CursorNotFound is the ground-truth signal. Pymongo also surfaces it via
    # OperationFailure(code=43) on some code paths, so check both.
    if error_class == "CursorNotFound" or error_code == 43:
        return FAILURE_CURSOR_LOST
    if _CURSOR_LOST_MESSAGE_RE.search(error_message or ""):
        return FAILURE_CURSOR_LOST
    if error_class in _TRANSIENT_NETWORK_CLASSES:
        return FAILURE_TRANSIENT_NETWORK
    if error_class == "OperationFailure" and _TRANSIENT_NETWORK_MESSAGE_RE.search(error_message or ""):
        return FAILURE_TRANSIENT_NETWORK
    return FAILURE_OTHER


@dataclass
class QueryResult:
    """Structured result for a single search-query attempt.

    A single ``QueryResult`` describes one attempt — either a one-shot query
    or a single page of a paging-mode run. Tests aggregate a list of these
    into a verdict.
    """

    success: bool
    started_at: float  # time.monotonic() value
    latency_ms: float  # wall-clock duration regardless of success
    returned_count: int = 0
    error_class: Optional[str] = None
    error_code: Optional[int] = None
    error_message: Optional[str] = None
    # Tri-state cache-hit hint:
    #   None  -> unknown (failed query, or paging on the first page where the
    #            heuristic doesn't apply yet).
    #   True  -> result strongly looks served from local buffer / cache.
    #   False -> result strongly looks served by a real upstream round-trip.
    cache_hit_hint: Optional[bool] = None
    query_kind: str = "search"  # "search" | "vector_search"
    query_token: Optional[str] = None  # cache-buster token, when applicable
    page_index: int = 0  # 0 for one-shot; page number (0-indexed) for paging
    cursor_id: Optional[int] = None  # paging only; 0 once the cursor closed
    # True iff the page was returned entirely from the client-local batch
    # buffer with no getMore round-trip to the server. None for one-shot.
    from_buffer_only: Optional[bool] = None
    # Failure classification — see FAILURE_* constants. None on success.
    failure_class: Optional[str] = None
    # Set to True when the result represents a transient_network failure
    # that survived an internal retry, OR a success that recovered from a
    # transient blip on the same call. Surfaced in the verdict so tests
    # can distinguish "clean signal" from "noisy signal" without needing
    # to introspect every QueryResult.
    noted: bool = False

    def __str__(self) -> str:
        bits = [
            f"page={self.page_index}",
            f"ok={self.success}",
            f"n={self.returned_count}",
            f"lat={self.latency_ms:.1f}ms",
        ]
        if self.cache_hit_hint is not None:
            bits.append(f"cache_hit={self.cache_hit_hint}")
        if self.failure_class:
            bits.append(f"failure={self.failure_class}")
        if self.noted:
            bits.append("noted")
        if self.error_class:
            bits.append(f"err={self.error_class}({self.error_code})")
        return " ".join(bits)


@dataclass
class ConnectivityVerdict:
    """Aggregate verdict over a sequence of ``QueryResult``s."""

    total: int = 0
    succeeded: int = 0
    failed: int = 0
    upstream_succeeded: int = 0  # success with cache_hit_hint=False
    cache_only_succeeded: int = 0  # success with cache_hit_hint=True
    unknown_succeeded: int = 0  # success with cache_hit_hint=None
    # Failure-class buckets. Sum of these equals ``failed``.
    cursor_lost: int = 0
    transient_network: int = 0
    other_failed: int = 0
    # Successes that survived a transient retry; subset of ``succeeded``.
    succeeded_with_retry: int = 0
    error_breakdown: dict[str, int] = field(default_factory=dict)
    first_error: Optional[str] = None
    last_error: Optional[str] = None

    @property
    def upstream_alive(self) -> bool:
        """True iff at least one query was confirmed to reach upstream."""
        return self.upstream_succeeded > 0

    @property
    def cursor_lost_observed(self) -> bool:
        """True iff at least one page surfaced a cursor-lost failure.

        This is the deliverable signal for tests that prove a long-living
        cursor's server-side state is gone (e.g. mongot pod restart) — a
        different semantics from a transient-network blip.
        """
        return self.cursor_lost > 0

    def as_dict(self) -> dict[str, Any]:
        return {
            "total": self.total,
            "succeeded": self.succeeded,
            "succeeded_with_retry": self.succeeded_with_retry,
            "failed": self.failed,
            "cursor_lost": self.cursor_lost,
            "transient_network": self.transient_network,
            "other_failed": self.other_failed,
            "upstream_succeeded": self.upstream_succeeded,
            "cache_only_succeeded": self.cache_only_succeeded,
            "unknown_succeeded": self.unknown_succeeded,
            "upstream_alive": self.upstream_alive,
            "cursor_lost_observed": self.cursor_lost_observed,
            "error_breakdown": dict(self.error_breakdown),
            "first_error": self.first_error,
            "last_error": self.last_error,
        }


class SearchConnectivityTool:
    """Reusable search connectivity tool driven by an existing ``SearchTester``.

    The tool intentionally does not own the MongoDB connection — it borrows the
    one already configured on the supplied ``SearchTester`` so e2e tests can
    keep using their existing TLS/auth fixtures without duplication.
    """

    def __init__(
        self,
        search_tester: SearchTester,
        db_name: str = "sample_mflix",
        col_name: str = "movies",
        cache_latency_threshold_ms: float = 5.0,
    ) -> None:
        self.search_tester = search_tester
        self.db_name = db_name
        self.col_name = col_name
        self.cache_latency_threshold_ms = cache_latency_threshold_ms

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    @property
    def _collection(self):
        return self.search_tester.client[self.db_name][self.col_name]

    @staticmethod
    def _classify_error(exc: BaseException) -> tuple[str, Optional[int]]:
        """Normalise a pymongo exception into ``(class_name, server_error_code)``.

        We use the class name as a coarse, stringly-typed bucket for verdict
        aggregation (``ConnectivityVerdict.error_breakdown`` keys on this) so
        callers can do ``error_breakdown["OperationFailure"] > 0`` style
        checks without importing pymongo. The server error code is only
        present for ``OperationFailure`` (network errors don't have codes).
        """
        klass = type(exc).__name__
        code: Optional[int] = None
        if isinstance(exc, pymongo.errors.OperationFailure):
            code = exc.code
        return klass, code

    def make_cache_buster_query(self, base_term: str = "movie") -> tuple[dict, str]:
        """Build a ``$search`` pipeline that mongot cannot serve from cache.

        The pipeline pairs a stable ``must`` clause with a unique random
        token in the ``should`` clause. ``must`` filters the result set —
        documents that don't match are excluded; ``should`` only contributes
        to the relevance score (a document still appears in the result even
        if it doesn't match ``should``, just with a lower score). So the
        random ``should`` token does not change *which* documents come
        back; what it changes is the **query identity** — mongot keys its
        per-query cache on the full query text, and a fresh token forces a
        real evaluation rather than a cache lookup. Returns
        ``(pipeline_stage, token)`` — the caller is responsible for
        building the full aggregation pipeline.
        """
        token = f"cb_{uuid.uuid4().hex[:12]}"
        stage = {
            "$search": {
                "compound": {
                    "must": [{"text": {"query": base_term, "path": "plot"}}],
                    "should": [{"text": {"query": token, "path": "plot"}}],
                }
            }
        }
        return stage, token

    @staticmethod
    def _cursor_buffer_size(cursor) -> Optional[int]:
        """Return the local buffer size of a pymongo cursor, or None if the
        attribute isn't available on this pymongo version.

        We use this to detect whether the next ``next()`` call will pop from
        the client-local batch or trigger a ``getMore`` to the server. Probing
        the buffer is the strongest signal we have for "this page actually
        contacted mongot/envoy" without doing intrusive network instrumentation.

        Preference order:
          1. ``cursor._has_next()`` — public-ish helper on pymongo's
             ``CommandCursor`` that reflects ``len(self._data) > 0`` without
             us reaching into private state. Returns a boolean, which we
             collapse to "0 buffered" or "1+ buffered".
          2. ``cursor._data`` — the underlying deque on pymongo 4.x
             ``CommandCursor``; returns the exact buffered count.
          3. ``cursor._CommandCursor__data`` — name-mangled fallback for
             defensive coverage of older releases.
        """
        if hasattr(cursor, "_has_next"):
            try:
                return 1 if cursor._has_next() else 0
            except Exception:  # pragma: no cover — defensive
                pass
        for attr in ("_data", "_CommandCursor__data"):
            if hasattr(cursor, attr):
                buf = getattr(cursor, attr)
                try:
                    return len(buf)
                except TypeError:
                    return None
        return None

    @staticmethod
    def _cursor_id(cursor) -> Optional[int]:
        for attr in ("cursor_id", "_id", "_CommandCursor__id"):
            if hasattr(cursor, attr):
                value = getattr(cursor, attr)
                if callable(value):  # defensive
                    try:
                        value = value()
                    except Exception:
                        continue
                if value is None:
                    return None
                try:
                    return int(value)
                except (TypeError, ValueError):
                    return None
        return None

    # ------------------------------------------------------------------
    # One-shot mode
    # ------------------------------------------------------------------

    def oneshot_search(
        self,
        query: Optional[dict] = None,
        cache_buster: bool = True,
        limit: int = 10,
        timeout_ms: Optional[int] = None,
    ) -> QueryResult:
        """Run a single ``$search`` aggregation and return a ``QueryResult``.

        Args:
            query: optional override for the ``$search`` stage. When ``None``
                and ``cache_buster`` is True, a cache-busted compound query is
                generated automatically. When ``None`` and ``cache_buster`` is
                False, a stable text search for "movie" is used.
            cache_buster: whether to inject a random token so mongot cannot
                serve from cache. Default True (recommended for availability
                tests). Ignored when ``query`` is supplied explicitly.
            limit: max number of documents the aggregation returns.
            timeout_ms: optional per-operation maxTimeMS.
        """
        token: Optional[str] = None
        if query is None:
            if cache_buster:
                stage, token = self.make_cache_buster_query()
            else:
                stage = {"$search": {"text": {"query": "movie", "path": "plot"}}}
        else:
            stage = query

        pipeline = [stage, {"$limit": limit}, {"$project": {"_id": 0, "title": 1}}]
        kwargs: dict[str, Any] = {}
        if timeout_ms is not None:
            kwargs["maxTimeMS"] = timeout_ms

        started = time.monotonic()
        try:
            docs = list(self._collection.aggregate(pipeline, **kwargs))
            elapsed_ms = (time.monotonic() - started) * 1000.0
            cache_hit = elapsed_ms < self.cache_latency_threshold_ms
            return QueryResult(
                success=True,
                started_at=started,
                latency_ms=elapsed_ms,
                returned_count=len(docs),
                cache_hit_hint=cache_hit,
                query_kind="search",
                query_token=token,
            )
        except pymongo.errors.PyMongoError as exc:
            elapsed_ms = (time.monotonic() - started) * 1000.0
            klass, code = self._classify_error(exc)
            msg = str(exc)
            logger.debug(f"oneshot_search failed in {elapsed_ms:.1f}ms: {klass}({code}) {exc}")
            return QueryResult(
                success=False,
                started_at=started,
                latency_ms=elapsed_ms,
                error_class=klass,
                error_code=code,
                error_message=msg,
                failure_class=classify_failure(klass, code, msg),
                query_kind="search",
                query_token=token,
            )

    def oneshot_vector_search(
        self,
        query_text: str = "spy thriller",
        index: str = "vector_auto_embed_index",
        path: str = "plot",
        limit: int = 10,
        timeout_ms: Optional[int] = None,
    ) -> QueryResult:
        """Run a single ``$vectorSearch`` query.

        Requires ``AI_MONGODB_EMBEDDING_QUERY_KEY`` to be set so the tool can
        ask Voyage for an embedding vector. Tests should gate on that env var
        with ``pytest.skipif`` rather than relying on this method to surface
        the absence as a query error.

        The ``index`` and ``path`` arguments default to the auto-embed index
        configured by ``SearchTester.create_auto_embedding_vector_search_index``,
        which is what the existing search e2es use. Explicit-vector indexes
        (e.g. those created via ``EmbeddedMoviesSearchHelper``) need a
        different ``index``/``path`` plus an externally-supplied vector — wire
        those through ``query`` on a follow-up if the need arises.
        """
        # We default to using the auto-embed index — for that index the
        # server resolves the embedding internally, so no Voyage call is
        # needed here. We still surface the env var note in the docstring
        # because callers using non-auto-embed indexes will need it.
        pipeline = [
            {
                "$vectorSearch": {
                    "index": index,
                    "path": path,
                    "query": query_text,
                    "numCandidates": max(limit * 15, 150),
                    "limit": limit,
                }
            },
            {
                "$project": {
                    "_id": 0,
                    "title": 1,
                    "score": {"$meta": "vectorSearchScore"},
                }
            },
        ]
        kwargs: dict[str, Any] = {}
        if timeout_ms is not None:
            kwargs["maxTimeMS"] = timeout_ms

        started = time.monotonic()
        try:
            docs = list(self._collection.aggregate(pipeline, **kwargs))
            elapsed_ms = (time.monotonic() - started) * 1000.0
            cache_hit = elapsed_ms < self.cache_latency_threshold_ms
            return QueryResult(
                success=True,
                started_at=started,
                latency_ms=elapsed_ms,
                returned_count=len(docs),
                cache_hit_hint=cache_hit,
                query_kind="vector_search",
            )
        except pymongo.errors.PyMongoError as exc:
            elapsed_ms = (time.monotonic() - started) * 1000.0
            klass, code = self._classify_error(exc)
            logger.debug(f"oneshot_vector_search failed in {elapsed_ms:.1f}ms: {klass}({code}) {exc}")
            return QueryResult(
                success=False,
                started_at=started,
                latency_ms=elapsed_ms,
                error_class=klass,
                error_code=code,
                error_message=str(exc),
                query_kind="vector_search",
            )

    # ------------------------------------------------------------------
    # Long-running paging mode
    # ------------------------------------------------------------------

    def paging_search(
        self,
        query: Optional[dict] = None,
        pages: int = 10,
        interval_seconds: float = 1.0,
        batch_size: int = 50,
        on_page: Optional[Callable[[QueryResult], None]] = None,
        stop_on_error: bool = False,
        timeout_ms: Optional[int] = None,
    ) -> list[QueryResult]:
        """Open a ``$search`` cursor and page through it.

        Convenience wrapper around ``paging_cursor_open`` +
        ``paging_cursor_read_pages``: opens the cursor, reads ``pages``
        pages, and closes. For tests that need to keep the same cursor
        alive across a fault (e.g. mongot pod restart), call those two
        helpers directly instead.
        """
        cursor, results, page_index_offset = self._paging_open_and_first_error(
            query=query,
            batch_size=batch_size,
            timeout_ms=timeout_ms,
        )
        if cursor is None:
            return results
        try:
            tail = self.paging_cursor_read_pages(
                cursor,
                pages=pages,
                interval_seconds=interval_seconds,
                batch_size=batch_size,
                on_page=on_page,
                stop_on_error=stop_on_error,
                first_page_index=page_index_offset,
            )
        finally:
            try:
                cursor.close()
            except Exception:  # pragma: no cover
                logger.debug("cursor.close() raised on cleanup")
        return results + tail

    def paging_cursor_open(
        self,
        query: Optional[dict] = None,
        batch_size: int = 50,
        timeout_ms: Optional[int] = None,
    ):
        """Open a ``$search`` aggregation cursor and return it.

        The caller takes ownership of the returned pymongo CommandCursor
        (must close it; ``paging_cursor_read_pages`` does not). Use this
        when a test needs to read pages, do something with the cursor's
        server-side state intact (e.g. restart mongot to invalidate it),
        then continue reading on the *same* cursor.
        """
        if batch_size < 1:
            raise ValueError(f"batch_size must be >= 1; got {batch_size}")
        stage = query if query is not None else self._default_paging_stage()
        pipeline = [stage, {"$project": {"_id": 0, "title": 1}}]
        agg_kwargs: dict[str, Any] = {"batchSize": batch_size}
        if timeout_ms is not None:
            agg_kwargs["maxTimeMS"] = timeout_ms
        return self._collection.aggregate(pipeline, **agg_kwargs)

    def paging_cursor_read_pages(
        self,
        cursor,
        pages: int,
        interval_seconds: float = 1.0,
        batch_size: int = 50,
        on_page: Optional[Callable[[QueryResult], None]] = None,
        stop_on_error: bool = False,
        first_page_index: int = 0,
        retry_transient_once: bool = True,
    ) -> list[QueryResult]:
        """Read ``pages`` pages from an already-open paging cursor.

        Each page pulls up to ``batch_size`` documents and records latency,
        success/failure, failure_class, cursor state, and the buffer-vs-
        getMore flag.

        ``first_page_index`` is the page index assigned to the first page
        produced by THIS call. Tests that read pages, do a fault, and read
        more should pass ``first_page_index=N`` on the second call where N
        is one past the last index from the first call — that way the page
        timeline in the resulting list is contiguous and easy to reason about.

        Retry semantics
        ---------------
        When ``retry_transient_once`` is True (the default), a single
        ``next(cursor)`` call that raises a ``transient_network`` failure
        (per ``classify_failure``) is retried one more time before being
        recorded. If the retry succeeds, the page result has ``noted=True``
        so the verdict can flag that there was a transient hiccup. If the
        retry fails, the page is recorded as a transient_network failure
        with ``noted=True`` to make clear this isn't an instantaneous
        signal but a persistent network condition.

        Cursor-lost and "other" failures are NEVER retried — they're
        recorded immediately because retrying on a dead cursor doesn't
        help, and "other" means the query itself is wrong.
        """
        if pages < 1:
            raise ValueError(f"pages must be >= 1; got {pages}")
        results: list[QueryResult] = []
        cursor_alive = True
        for page_offset in range(pages):
            page_index = first_page_index + page_offset
            page_started = time.monotonic()
            docs: list[Any] = []
            page_error: Optional[QueryResult] = None
            had_transient_retry = False
            any_getmore = False
            buffer_probed_at_least_once = False

            for _ in range(batch_size):
                if not cursor_alive:
                    break
                pre_buffer = self._cursor_buffer_size(cursor)
                if pre_buffer is not None:
                    buffer_probed_at_least_once = True
                    if pre_buffer == 0:
                        any_getmore = True
                try:
                    docs.append(next(cursor))
                    continue
                except StopIteration:
                    cursor_alive = False
                    break
                except pymongo.errors.PyMongoError as exc:
                    klass, code = self._classify_error(exc)
                    msg = str(exc)
                    fclass = classify_failure(klass, code, msg)

                # Retry-once-noted only for transient_network. Cursor-lost
                # and other failures fall through to record immediately.
                if (
                    retry_transient_once
                    and fclass == FAILURE_TRANSIENT_NETWORK
                    and not had_transient_retry
                ):
                    had_transient_retry = True
                    logger.debug(
                        f"paging_cursor_read_pages: transient_network on page={page_index} "
                        f"({klass}({code}) {msg!r}); retrying once"
                    )
                    try:
                        docs.append(next(cursor))
                        continue
                    except StopIteration:
                        cursor_alive = False
                        break
                    except pymongo.errors.PyMongoError as exc2:
                        klass, code = self._classify_error(exc2)
                        msg = str(exc2)
                        fclass = classify_failure(klass, code, msg)

                elapsed = (time.monotonic() - page_started) * 1000.0
                page_error = QueryResult(
                    success=False,
                    started_at=page_started,
                    latency_ms=elapsed,
                    error_class=klass,
                    error_code=code,
                    error_message=msg,
                    failure_class=fclass,
                    noted=had_transient_retry,
                    query_kind="search",
                    page_index=page_index,
                    cursor_id=self._cursor_id(cursor),
                )
                cursor_alive = False
                break

            elapsed_ms = (time.monotonic() - page_started) * 1000.0

            if page_error is not None:
                result = page_error
            else:
                # Page 0 of a freshly-opened cursor corresponds to its
                # firstBatch and required a round-trip to open + fetch.
                # In the cursor-reuse case (first_page_index > 0) we lose
                # that guarantee — by then the cursor may have buffered
                # data — so we fall back to the buffer-probe heuristic.
                if page_index == 0:
                    cache_hit = False
                elif not buffer_probed_at_least_once:
                    cache_hit = None
                else:
                    cache_hit = not any_getmore
                result = QueryResult(
                    success=True,
                    started_at=page_started,
                    latency_ms=elapsed_ms,
                    returned_count=len(docs),
                    cache_hit_hint=cache_hit,
                    query_kind="search",
                    page_index=page_index,
                    cursor_id=self._cursor_id(cursor),
                    from_buffer_only=(None if not buffer_probed_at_least_once else not any_getmore),
                    noted=had_transient_retry,
                )

            results.append(result)
            if on_page is not None:
                try:
                    on_page(result)
                except Exception:  # pragma: no cover — purely callback-side
                    logger.exception("on_page callback raised; continuing")

            if not result.success and stop_on_error:
                break
            if not cursor_alive:
                break
            if interval_seconds > 0 and page_offset + 1 < pages:
                time.sleep(interval_seconds)

        return results

    @staticmethod
    def _default_paging_stage() -> dict:
        """Default ``$search`` stage used when callers don't supply one.

        A wildcard text search over ``title`` matches a large fraction of
        the sample-mflix corpus, giving paging tests plenty of data.
        """
        return {
            "$search": {
                "wildcard": {
                    "query": "*",
                    "path": "title",
                    "allowAnalyzedField": True,
                }
            }
        }

    def _paging_open_and_first_error(
        self,
        query: Optional[dict],
        batch_size: int,
        timeout_ms: Optional[int],
    ) -> tuple[Any, list[QueryResult], int]:
        """Internal helper for ``paging_search``: open the cursor, or
        return a synthetic page-0 failure if ``aggregate()`` itself raises.

        Returns ``(cursor, error_results, next_page_index)``.
        On success: ``(cursor, [], 0)``.
        On failure: ``(None, [<one failure result>], -)``.
        """
        try:
            cursor = self.paging_cursor_open(query=query, batch_size=batch_size, timeout_ms=timeout_ms)
            return cursor, [], 0
        except pymongo.errors.PyMongoError as exc:
            klass, code = self._classify_error(exc)
            msg = str(exc)
            logger.debug(f"paging_search aggregate() failed: {klass}({code}) {exc}")
            return (
                None,
                [
                    QueryResult(
                        success=False,
                        started_at=time.monotonic(),
                        latency_ms=0.0,
                        error_class=klass,
                        error_code=code,
                        error_message=msg,
                        failure_class=classify_failure(klass, code, msg),
                        query_kind="search",
                        page_index=0,
                    )
                ],
                0,
            )

    # ------------------------------------------------------------------
    # Sentinel propagation — strongest "really upstream" check
    # ------------------------------------------------------------------

    def insert_sentinel(self, prefix: str = "sentinel") -> str:
        """Insert a sentinel document and return its (fully random) title.

        The title is generated as ``{prefix}_{uuid.uuid4().hex[:12]}`` so
        every call produces a unique, server-unseen value. After mongot has
        rebuilt the search index against the underlying collection,
        ``search_for_sentinel`` will find this document — if it doesn't,
        mongot is either stale or unreachable. This is the strongest "the
        upstream search index is actually current" check we can run from a
        client.

        Note: the index-rebuild is asynchronous; callers must poll for the
        sentinel via ``search_for_sentinel`` (which polls internally) or
        their own equivalent — never sleep a fixed amount and assume the
        document is queryable, that's racy.
        """
        title = f"{prefix}_{uuid.uuid4().hex[:12]}"
        self._collection.insert_one({"title": title, "plot": "sentinel doc"})
        return title

    def search_for_sentinel(
        self,
        title: str,
        overall_timeout_seconds: float = 60.0,
        poll_interval_seconds: float = 1.0,
        per_query_timeout_ms: int = 2000,
    ) -> QueryResult:
        """Poll ``$search`` for a sentinel until it appears or we time out.

        mongot rebuilds the search index asynchronously after a write — the
        sentinel doc is not queryable immediately. We poll instead of
        sleeping a fixed amount because the rebuild latency is variable
        (especially under load) and any single fixed sleep is either
        wasteful or racy. Returns the first ``QueryResult`` whose
        ``returned_count > 0`` (success), or the last attempt's result if
        the overall timeout elapses without the sentinel appearing.
        """
        stage = {"$search": {"text": {"query": title, "path": "title"}}}
        deadline = time.monotonic() + overall_timeout_seconds
        while True:
            result = self.oneshot_search(query=stage, cache_buster=False, limit=5, timeout_ms=per_query_timeout_ms)
            if result.success and result.returned_count > 0:
                return result
            if time.monotonic() >= deadline:
                return result
            time.sleep(poll_interval_seconds)

    # ------------------------------------------------------------------
    # Verdict
    # ------------------------------------------------------------------

    def verdict(self, results: list[QueryResult]) -> ConnectivityVerdict:
        """Aggregate a list of ``QueryResult``s into a single verdict.

        Beyond the cache-hit / success-vs-failure split, the verdict also
        breaks failures down by ``failure_class`` (cursor_lost /
        transient_network / other). ``cursor_lost`` is the load-bearing
        signal for tests that prove a server-side cursor is gone (mongot
        pod restart); ``transient_network`` represents recoverable blips
        and is informational rather than diagnostic.
        """
        v = ConnectivityVerdict()
        for r in results:
            v.total += 1
            if r.success:
                v.succeeded += 1
                if r.noted:
                    v.succeeded_with_retry += 1
                if r.cache_hit_hint is True:
                    v.cache_only_succeeded += 1
                elif r.cache_hit_hint is False:
                    v.upstream_succeeded += 1
                else:
                    v.unknown_succeeded += 1
            else:
                v.failed += 1
                if r.failure_class == FAILURE_CURSOR_LOST:
                    v.cursor_lost += 1
                elif r.failure_class == FAILURE_TRANSIENT_NETWORK:
                    v.transient_network += 1
                else:
                    v.other_failed += 1
                klass = r.error_class or "Unknown"
                v.error_breakdown[klass] = v.error_breakdown.get(klass, 0) + 1
                msg = r.error_message or klass
                if v.first_error is None:
                    v.first_error = msg
                v.last_error = msg
        return v


# ----------------------------------------------------------------------
# Module-level helpers — mostly to make pytest gating ergonomic.
# ----------------------------------------------------------------------


def voyage_query_key_available() -> bool:
    """True if the Voyage embedding-query API key is in the environment.

    Use as ``pytest.skipif(not voyage_query_key_available(), reason=...)`` on
    tests that exercise vector search end-to-end with externally-resolved
    embeddings.
    """
    return bool(os.getenv(EMBEDDING_QUERY_KEY_ENV_VAR))


def fetch_voyage_embedding(query_text: str, timeout: float = 10.0) -> list[float]:
    """Resolve a query text to an embedding vector via the Voyage proxy.

    Mirrors ``EmbeddedMoviesSearchHelper.generate_query_vector`` so callers of
    this module can stay self-contained. Raises ValueError if the API key env
    var is unset; raises ``requests.HTTPError`` on non-2xx responses.
    """
    api_key = os.getenv(EMBEDDING_QUERY_KEY_ENV_VAR)
    if not api_key:
        raise ValueError(f"Missing required environment variable: {EMBEDDING_QUERY_KEY_ENV_VAR}")
    response = requests.post(
        VOYAGE_EMBEDDING_ENDPOINT,
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        json={
            "model": VOYAGE_MODEL,
            "input": [query_text],
            "output_dimension": VOYAGE_DIMENSIONS,
        },
        timeout=timeout,
    )
    response.raise_for_status()
    return response.json()["data"][0]["embedding"]
