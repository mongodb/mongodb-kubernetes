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

How "did this query actually hit mongod?" is determined
-------------------------------------------------------

mongod and pymongo both buffer aggressively. A naïve "did the query succeed?"
check can stay green long after upstream (envoy/mongot) has gone away — pages
served from pymongo's local cursor batch don't touch the wire at all, and
pages served from mongod's internal ``TaskExecutorCursor::_batch`` don't
touch mongot.

We answer the client-side half of that question deterministically by attaching
a pymongo ``CommandListener`` to the underlying ``MongoClient``. Per the
follow-up investigation in
``tmp/search-caching-investigation/observability-followup.md``, every
wire-protocol round-trip (``aggregate``, ``getMore``, ``killCursors``) emits
exactly one ``CommandStartedEvent`` + one ``CommandSucceededEvent`` /
``CommandFailedEvent``. Pages served entirely from the pymongo local cursor
buffer emit **zero** events — so a per-page count of started events is the
ground-truth predicate "did this page actually hit mongod?".

Each ``QueryResult`` therefore carries:

- ``mongod_wire_ops``: number of wire commands issued during the call. 0 means
  the page was served from pymongo's local buffer alone; ``>= 1`` means it
  actually went to mongod. Replaces the old latency-band / buffer-probe
  heuristic.
- ``lsids``: the logical-session-id UUIDs observed on this attempt. These are
  the exact bytes mongod logs as ``attr.command.lsid.id`` in every COMMAND
  ``Slow query`` record, so the analyzer can join client events to mongod
  log lines without any time-window heuristic.
- ``server_connection_ids``: mongod's per-connection counter (its ``conn<N>``
  context). Stable across the cursor's lifetime — every aggregate/getMore/
  killCursors on one cursor lands on the same mongod connection.

The strongest "really hit upstream mongot" assertion is still
``insert_sentinel`` + ``search_for_sentinel`` — a sentinel doc only becomes
searchable after mongot has reindexed against the underlying collection.

No unit tests with mocks — verified end-to-end on real systems via the e2e
tests under ``tests/search/``.
"""

from __future__ import annotations

import os
import re
import threading
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, Callable, Optional

import pymongo
import pymongo.errors
import pymongo.monitoring
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
    r"cursor id .*?(not found|was killed)|" r"remote error from mongot|" r"rst_stream",
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


# ----------------------------------------------------------------------
# pymongo CommandListener integration
# ----------------------------------------------------------------------


@dataclass
class ClientWireOp:
    """One wire-protocol command observed by the pymongo CommandListener.

    These are emitted by pymongo for every command that actually goes to the
    network — buffer-popped pages emit nothing. The fields below mirror what
    the analyzer's ``parse_client_wire_ops`` can join against mongod log
    records: ``lsid`` and ``server_connection_id`` are the load-bearing
    cross-side join keys.
    """

    phase: str  # "started" | "succeeded" | "failed"
    command_name: str
    request_id: int
    operation_id: Optional[int]
    timestamp: float  # time.monotonic() — wall-clock-independent
    server_connection_id: Optional[int] = None
    lsid: Optional[str] = None  # hex form of the lsid UUID, or None
    cursor_id: Optional[int] = None  # set on getMore (cursor in cmd) / aggregate reply
    duration_micros: Optional[int] = None  # CommandSucceededEvent / CommandFailedEvent
    n_returned: Optional[int] = None  # extracted from reply.cursor.firstBatch/nextBatch
    database_name: Optional[str] = None
    failure: Optional[str] = None  # error description for "failed"


class _RecordingCommandListener(pymongo.monitoring.CommandListener):
    """CommandListener that records every wire op into a shared buffer.

    The same listener instance lives for the life of the underlying
    ``MongoClient``. ``SearchConnectivityTool`` snapshots+clears the buffer
    around each call so the per-result event list is scoped to that call.

    Thread-safe because pymongo can fire events from background threads
    (heartbeats, connection-pool maintenance) — we don't actually record
    those, but the records list still needs a lock for concurrent appends
    from the application thread vs any pymongo internal threads.
    """

    def __init__(self) -> None:
        self._records: list[ClientWireOp] = []
        self._lock = threading.Lock()

    # --- pymongo callback surface ---

    def started(self, event: pymongo.monitoring.CommandStartedEvent) -> None:
        self._record(self._from_started(event))

    def succeeded(self, event: pymongo.monitoring.CommandSucceededEvent) -> None:
        self._record(self._from_succeeded(event))

    def failed(self, event: pymongo.monitoring.CommandFailedEvent) -> None:
        self._record(self._from_failed(event))

    # --- helpers ---

    def _record(self, op: ClientWireOp) -> None:
        with self._lock:
            self._records.append(op)

    def snapshot_since(self, marker: int) -> list[ClientWireOp]:
        """Return every record appended at or after ``marker`` and the new tail.

        Returns a snapshot of records[marker:]. Callers pass the size of
        the records list at the call-start marker; this is cheaper than
        per-call buffer clears (no contention on the application thread
        when other threads are also recording).
        """
        with self._lock:
            return list(self._records[marker:])

    def current_marker(self) -> int:
        """Return the current length of the records buffer.

        Use at the start of a call; pass to ``snapshot_since`` at the end.
        """
        with self._lock:
            return len(self._records)

    @staticmethod
    def _extract_lsid(command: Any) -> Optional[str]:
        lsid = (command or {}).get("lsid")
        if not lsid:
            return None
        sid = lsid.get("id") if isinstance(lsid, dict) else None
        if sid is None:
            return None
        # pymongo represents the UUID as a bson Binary; hex() works on both
        # Binary (subtype 4) and bytes.
        try:
            return sid.hex() if hasattr(sid, "hex") else str(sid)
        except Exception:  # pragma: no cover — defensive
            return None

    @staticmethod
    def _extract_cursor_id_from_cmd(command_name: str, command: Any) -> Optional[int]:
        if not isinstance(command, dict):
            return None
        if command_name == "getMore":
            return command.get("getMore")
        if command_name == "killCursors":
            ids = command.get("cursors") or []
            return ids[0] if ids else None
        return None

    @staticmethod
    def _extract_reply_cursor(reply: Any) -> tuple[Optional[int], Optional[int]]:
        if not isinstance(reply, dict):
            return None, None
        cursor = reply.get("cursor") or {}
        if not isinstance(cursor, dict):
            return None, None
        cid = cursor.get("id")
        batch = cursor.get("nextBatch") or cursor.get("firstBatch") or []
        n = len(batch) if isinstance(batch, list) else None
        return cid, n

    @classmethod
    def _from_started(cls, event: pymongo.monitoring.CommandStartedEvent) -> ClientWireOp:
        return ClientWireOp(
            phase="started",
            command_name=event.command_name,
            request_id=event.request_id,
            operation_id=event.operation_id,
            timestamp=time.monotonic(),
            server_connection_id=getattr(event, "server_connection_id", None),
            lsid=cls._extract_lsid(event.command),
            cursor_id=cls._extract_cursor_id_from_cmd(event.command_name, event.command),
            database_name=event.database_name,
        )

    @classmethod
    def _from_succeeded(cls, event: pymongo.monitoring.CommandSucceededEvent) -> ClientWireOp:
        cid, n_returned = cls._extract_reply_cursor(event.reply)
        return ClientWireOp(
            phase="succeeded",
            command_name=event.command_name,
            request_id=event.request_id,
            operation_id=event.operation_id,
            timestamp=time.monotonic(),
            server_connection_id=getattr(event, "server_connection_id", None),
            duration_micros=event.duration_micros,
            cursor_id=cid,
            n_returned=n_returned,
        )

    @classmethod
    def _from_failed(cls, event: pymongo.monitoring.CommandFailedEvent) -> ClientWireOp:
        return ClientWireOp(
            phase="failed",
            command_name=event.command_name,
            request_id=event.request_id,
            operation_id=event.operation_id,
            timestamp=time.monotonic(),
            server_connection_id=getattr(event, "server_connection_id", None),
            duration_micros=event.duration_micros,
            failure=str(event.failure)[:200],
        )


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
    query_kind: str = "search"  # "search" | "vector_search"
    query_token: Optional[str] = None  # cache-buster token, when applicable
    page_index: int = 0  # 0 for one-shot; page number (0-indexed) for paging
    cursor_id: Optional[int] = None  # paging only; 0 once the cursor closed
    # Count of wire-protocol commands (CommandStartedEvent) observed by the
    # pymongo CommandListener while this attempt was in flight. The
    # ground-truth replacement for the old ``cache_hit_hint`` heuristic:
    # ``mongod_wire_ops == 0`` means the page was popped from pymongo's
    # local cursor buffer (no wire op), ``mongod_wire_ops >= 1`` means at
    # least one command (aggregate/getMore/killCursors) reached mongod.
    mongod_wire_ops: int = 0
    # The wire ops themselves (started/succeeded/failed records). Surface
    # for tests/analyzer that want to inspect lsid / server_connection_id /
    # cursor_id_in_reply / per-op duration_micros.
    wire_ops: list[ClientWireOp] = field(default_factory=list)
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
            f"wire={self.mongod_wire_ops}",
        ]
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
    # Successes split by whether they hit mongod over the wire. ``hit_mongod``
    # counts attempts with ``mongod_wire_ops > 0`` (a real wire round-trip);
    # ``buffer_only`` counts attempts that were served entirely from
    # pymongo's local cursor buffer with no wire op at all.
    hit_mongod: int = 0
    buffer_only: int = 0
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
    def hit_mongod_observed(self) -> bool:
        """True iff at least one attempt actually issued a mongod wire op.

        This is the load-bearing "did the harness exercise the server?"
        predicate — without at least one wire op, every other assertion
        is talking about the local cursor buffer, not the cluster.
        """
        return self.hit_mongod > 0

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
            "hit_mongod": self.hit_mongod,
            "buffer_only": self.buffer_only,
            "hit_mongod_observed": self.hit_mongod_observed,
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

    A pymongo ``CommandListener`` is registered on the underlying client at
    tool-construction time and stays attached for the life of the tool. Every
    ``oneshot_search`` / page of ``paging_search`` is bracketed by a "what
    new events appeared?" snapshot so the per-result ``mongod_wire_ops`` /
    ``wire_ops`` fields are scoped to that attempt.
    """

    def __init__(
        self,
        search_tester: SearchTester,
        db_name: str = "sample_mflix",
        col_name: str = "movies",
    ) -> None:
        self.search_tester = search_tester
        self.db_name = db_name
        self.col_name = col_name
        # Register the listener. pymongo only accepts event_listeners at
        # MongoClient construction time, so we need to ensure the client is
        # initialised here BEFORE any other code grabs the property. We
        # reconstruct the client with the listener attached so the same
        # SearchTester can be reused by callers that bypass this tool.
        self._listener = _RecordingCommandListener()
        self._install_listener_on_search_tester(search_tester, self._listener)

    @staticmethod
    def _install_listener_on_search_tester(
        search_tester: SearchTester,
        listener: pymongo.monitoring.CommandListener,
    ) -> None:
        """Attach ``listener`` to the underlying MongoClient by rebuilding it.

        ``pymongo.MongoClient`` only consumes ``event_listeners`` at
        construction time. ``MongoTester`` lazily builds its client via
        ``_init_client`` on first ``client`` access; we override that here
        by closing any pre-existing client and forcing a rebuild with the
        listener attached. Subsequent ``search_tester.client`` accesses
        will see the listener-wired client.
        """
        existing = getattr(search_tester, "_client", None)
        if existing is not None:
            try:
                existing.close()
            except Exception:  # pragma: no cover — defensive
                pass
        new_client = pymongo.MongoClient(
            search_tester.cnx_string,
            event_listeners=[listener],
            **search_tester.default_opts,
        )
        search_tester.client = new_client

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
    # Listener event capture
    # ------------------------------------------------------------------

    def _begin_capture(self) -> int:
        """Return a snapshot marker to use for ``_end_capture``."""
        return self._listener.current_marker()

    def _end_capture(self, marker: int) -> list[ClientWireOp]:
        """Return every wire op event appended since ``marker``."""
        return self._listener.snapshot_since(marker)

    @staticmethod
    def _count_wire_ops(events: list[ClientWireOp]) -> int:
        """Count ``CommandStartedEvent``s in a list of records.

        Each wire-protocol round-trip emits exactly one ``started`` event;
        the matching ``succeeded`` / ``failed`` doesn't represent a new
        round-trip. We use the count of ``started`` records as the
        "this attempt issued N wire ops" predicate.
        """
        return sum(1 for e in events if e.phase == "started")

    @property
    def listener(self) -> _RecordingCommandListener:
        """Expose the underlying CommandListener for advanced callers.

        Exposed so failure-mode tests / probe scripts can iterate over
        every recorded event (not just the per-result subset) when they
        want to render a full cross-side timeline via the analyzer.
        """
        return self._listener

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

        marker = self._begin_capture()
        started = time.monotonic()
        try:
            docs = list(self._collection.aggregate(pipeline, **kwargs))
            elapsed_ms = (time.monotonic() - started) * 1000.0
            wire_ops = self._end_capture(marker)
            return QueryResult(
                success=True,
                started_at=started,
                latency_ms=elapsed_ms,
                returned_count=len(docs),
                mongod_wire_ops=self._count_wire_ops(wire_ops),
                wire_ops=wire_ops,
                query_kind="search",
                query_token=token,
            )
        except pymongo.errors.PyMongoError as exc:
            elapsed_ms = (time.monotonic() - started) * 1000.0
            wire_ops = self._end_capture(marker)
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
                mongod_wire_ops=self._count_wire_ops(wire_ops),
                wire_ops=wire_ops,
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

        marker = self._begin_capture()
        started = time.monotonic()
        try:
            docs = list(self._collection.aggregate(pipeline, **kwargs))
            elapsed_ms = (time.monotonic() - started) * 1000.0
            wire_ops = self._end_capture(marker)
            return QueryResult(
                success=True,
                started_at=started,
                latency_ms=elapsed_ms,
                returned_count=len(docs),
                mongod_wire_ops=self._count_wire_ops(wire_ops),
                wire_ops=wire_ops,
                query_kind="vector_search",
            )
        except pymongo.errors.PyMongoError as exc:
            elapsed_ms = (time.monotonic() - started) * 1000.0
            wire_ops = self._end_capture(marker)
            klass, code = self._classify_error(exc)
            logger.debug(f"oneshot_vector_search failed in {elapsed_ms:.1f}ms: {klass}({code}) {exc}")
            return QueryResult(
                success=False,
                started_at=started,
                latency_ms=elapsed_ms,
                error_class=klass,
                error_code=code,
                error_message=str(exc),
                mongod_wire_ops=self._count_wire_ops(wire_ops),
                wire_ops=wire_ops,
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
        success/failure, failure_class, cursor state, and the count of
        wire-protocol round-trips issued during the page (``mongod_wire_ops``)
        — the ground-truth replacement for the old buffer-probe heuristic.

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
            marker = self._begin_capture()
            docs: list[Any] = []
            page_error: Optional[QueryResult] = None
            had_transient_retry = False

            for _ in range(batch_size):
                if not cursor_alive:
                    break
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
                if retry_transient_once and fclass == FAILURE_TRANSIENT_NETWORK and not had_transient_retry:
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
                wire_ops = self._end_capture(marker)
                page_error = QueryResult(
                    success=False,
                    started_at=page_started,
                    latency_ms=elapsed,
                    error_class=klass,
                    error_code=code,
                    error_message=msg,
                    failure_class=fclass,
                    noted=had_transient_retry,
                    mongod_wire_ops=self._count_wire_ops(wire_ops),
                    wire_ops=wire_ops,
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
                wire_ops = self._end_capture(marker)
                result = QueryResult(
                    success=True,
                    started_at=page_started,
                    latency_ms=elapsed_ms,
                    returned_count=len(docs),
                    mongod_wire_ops=self._count_wire_ops(wire_ops),
                    wire_ops=wire_ops,
                    query_kind="search",
                    page_index=page_index,
                    cursor_id=self._cursor_id(cursor),
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
        marker = self._begin_capture()
        try:
            cursor = self.paging_cursor_open(query=query, batch_size=batch_size, timeout_ms=timeout_ms)
            return cursor, [], 0
        except pymongo.errors.PyMongoError as exc:
            klass, code = self._classify_error(exc)
            msg = str(exc)
            wire_ops = self._end_capture(marker)
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
                        mongod_wire_ops=self._count_wire_ops(wire_ops),
                        wire_ops=wire_ops,
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

        Splits successes by ``mongod_wire_ops`` (was the page actually a
        wire round-trip?) and failures by ``failure_class``
        (cursor_lost / transient_network / other). ``cursor_lost`` is the
        load-bearing signal for tests that prove a server-side cursor is
        gone (mongot pod restart); ``transient_network`` represents
        recoverable blips and is informational rather than diagnostic.
        """
        v = ConnectivityVerdict()
        for r in results:
            v.total += 1
            if r.success:
                v.succeeded += 1
                if r.noted:
                    v.succeeded_with_retry += 1
                if r.mongod_wire_ops > 0:
                    v.hit_mongod += 1
                else:
                    v.buffer_only += 1
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
