"""Search connectivity tool for MongoDBSearch availability testing.

Issues ``$search`` queries against an MCK-deployed cluster and returns
structured per-query results. Two modes: one-shot and long-running paging.
"""

from __future__ import annotations

import re
import time
import uuid
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any, Callable, Optional

import pymongo
import pymongo.errors
from kubernetes import client
from kubetester import list_matching_pods, pod_is_ready
from kubetester.kubetester import run_periodically
from pymongo.read_preferences import Nearest
from tests import test_logger
from tests.common.search.search_tester import SearchTester

if TYPE_CHECKING:
    from kubetester.mongodb_search import MongoDBSearch
    from kubetester.multicluster_client import MultiClusterClient

logger = test_logger.get_test_logger(__name__)


FAILURE_CURSOR_LOST = "cursor_lost"
FAILURE_TRANSIENT_NETWORK = "transient_network"
# mongot was reached but rejected the query because the index isn't servable
# (INITIAL_SYNC / NOT_STARTED / UNKNOWN / FAILED). This is the bad onboarding
# mode: a Ready-latched mongot serving a still-syncing range. We must never see
# it — the only acceptable onboarding gap is "no mongot reachable at all".
FAILURE_INDEX_UNAVAILABLE = "index_unavailable"
# mongod has no mongotHost on the shard at all (search not enabled there).
FAILURE_SEARCH_NOT_ENABLED = "search_not_enabled"
# The $search fan-out stalled to MaxTimeMSExpired: mongotHost set but nothing
# answers (e.g. Envoy not deployed yet) — or, indistinguishably at this layer, a
# reachable mongot that stalls instead of rejecting. Treated as a clean gap;
# only an explicit index-state rejection proves the bad Ready-while-syncing mode.
FAILURE_MONGOT_UNREACHABLE = "mongot_unreachable"
FAILURE_OTHER = "other"

SEARCH_OWNER_NAME_LABEL = "mongodb.com/search-name"
SEARCH_OWNER_NAMESPACE_LABEL = "mongodb.com/search-namespace"

_CURSOR_LOST_MESSAGE_RE = re.compile(
    r"cursor id .*?(not found|was killed)|remote error from mongot|rst_stream",
    re.IGNORECASE,
)
_TRANSIENT_NETWORK_MESSAGE_RE = re.compile(
    r"no healthy upstream|connection refused|connection reset|broken pipe",
    re.IGNORECASE,
)
# mongot index-state rejection from LuceneSearchIndex.throwIfUnavailableForQuerying.
# Generic "while in state <X>": queries only ever get this for unservable states,
# so match any state token — a rewording/new state must not slip past the tripwire.
_INDEX_UNAVAILABLE_MESSAGE_RE = re.compile(
    r"while in state \w+|INITIAL_SYNC",
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
    """Map ``(class, code, message)`` to a failure class.

    Precedence: explicit cursor-loss (code 43) first, then the index_unavailable
    tripwire, then the cursor-lost message heuristic — so a mongot index-state
    rejection is never misfiled under cursor_lost or transient_network.
    """
    msg = error_message or ""
    if error_class == "CursorNotFound" or error_code == 43:
        return FAILURE_CURSOR_LOST
    # Tripwire first: mongod may wrap a mongot rejection as "remote error from
    # mongot :: ... while in state INITIAL_SYNC" — that must never land in
    # cursor_lost and bypass the index_unavailable==0 invariant.
    if _INDEX_UNAVAILABLE_MESSAGE_RE.search(msg):
        return FAILURE_INDEX_UNAVAILABLE
    if _CURSOR_LOST_MESSAGE_RE.search(msg):
        return FAILURE_CURSOR_LOST
    # SearchNotEnabled (31082): the shard's mongod has no mongotHost.
    if error_code == 31082:
        return FAILURE_SEARCH_NOT_ENABLED
    if error_class in _TRANSIENT_NETWORK_CLASSES:
        return FAILURE_TRANSIENT_NETWORK
    if error_class == "OperationFailure" and _TRANSIENT_NETWORK_MESSAGE_RE.search(msg):
        return FAILURE_TRANSIENT_NETWORK
    # MaxTimeMSExpired (50): the fan-out stalled because the shard's mongotHost
    # has nothing answering (Envoy proxy not yet deployed). Checked after
    # index_unavailable so a real INITIAL_SYNC rejection is never masked.
    if error_code == 50:
        return FAILURE_MONGOT_UNREACHABLE
    return FAILURE_OTHER


@dataclass
class QueryResult:
    """Result of one search-query attempt (one-shot or one page)."""

    success: bool
    latency_ms: float
    returned_count: int = 0
    error_class: Optional[str] = None
    error_code: Optional[int] = None
    error_message: Optional[str] = None
    failure_class: Optional[str] = None
    page_index: int = 0

    def __str__(self) -> str:
        bits = [
            f"page={self.page_index}",
            f"ok={self.success}",
            f"n={self.returned_count}",
            f"lat={self.latency_ms:.1f}ms",
        ]
        if self.failure_class:
            bits.append(f"failure={self.failure_class}")
        if self.error_class:
            bits.append(f"err={self.error_class}({self.error_code})")
        return " ".join(bits)


@dataclass
class ConnectivityVerdict:
    """Aggregate verdict over a sequence of ``QueryResult``s."""

    # Operation counts. One operation == one recorded query attempt: a single
    # page read in paging mode, or one oneshot_search. succeeded + failed ==
    # total_operations; cursor_lost / transient_network / other_failed
    # partition the failed operations by failure class.
    total_operations: int = 0
    succeeded: int = 0
    failed: int = 0
    cursor_lost: int = 0
    transient_network: int = 0
    index_unavailable: int = 0
    search_not_enabled: int = 0
    mongot_unreachable: int = 0
    other_failed: int = 0
    # Cumulative records returned by the tool across the whole session, summed
    # over every cursor roll.
    total_returned_records: int = 0
    # Paging only, set by the background tester:
    #  * cursor_reopens — times the cursor was reopened after the initial open
    #    (each follows a failure, exhaustion, or paging_reset_every roll).
    #  * current_cursor_records — records fetched on the *current* cursor;
    #    resets to 0 on each reopen. 0 for single-cursor (non-paging) verdicts.
    cursor_reopens: int = 0
    current_cursor_records: int = 0
    error_breakdown: dict[str, int] = field(default_factory=dict)
    first_error: Optional[str] = None
    last_error: Optional[str] = None

    def as_dict(self) -> dict[str, Any]:
        return {
            "total_operations": self.total_operations,
            "succeeded": self.succeeded,
            "failed": self.failed,
            "cursor_lost": self.cursor_lost,
            "transient_network": self.transient_network,
            "index_unavailable": self.index_unavailable,
            "search_not_enabled": self.search_not_enabled,
            "mongot_unreachable": self.mongot_unreachable,
            "other_failed": self.other_failed,
            "total_returned_records": self.total_returned_records,
            "cursor_reopens": self.cursor_reopens,
            "current_cursor_records": self.current_cursor_records,
            "error_breakdown": dict(self.error_breakdown),
            "first_error": self.first_error,
            "last_error": self.last_error,
        }


def aggregate_verdicts(verdicts: list[ConnectivityVerdict]) -> ConnectivityVerdict:
    """Sum a set of per-probe verdicts into one fleet-wide verdict.

    Counts add; error_breakdown maps merge; first_error keeps the first non-None
    first_error in fleet order, last_error the latest non-None last_error seen.
    """
    agg = ConnectivityVerdict()
    for v in verdicts:
        agg.total_operations += v.total_operations
        agg.succeeded += v.succeeded
        agg.failed += v.failed
        agg.cursor_lost += v.cursor_lost
        agg.transient_network += v.transient_network
        agg.index_unavailable += v.index_unavailable
        agg.search_not_enabled += v.search_not_enabled
        agg.mongot_unreachable += v.mongot_unreachable
        agg.other_failed += v.other_failed
        agg.total_returned_records += v.total_returned_records
        agg.cursor_reopens += v.cursor_reopens
        agg.current_cursor_records += v.current_cursor_records
        for klass, count in v.error_breakdown.items():
            agg.error_breakdown[klass] = agg.error_breakdown.get(klass, 0) + count
        if agg.first_error is None:
            agg.first_error = v.first_error
        if v.last_error is not None:
            agg.last_error = v.last_error
    return agg


class SearchConnectivityTool:
    """Drives ``$search`` queries against an MCK MongoDBSearch deployment."""

    def __init__(
        self,
        search_tester: SearchTester,
        db_name: str = "sample_mflix",
        col_name: str = "movies",
    ) -> None:
        self.search_tester = search_tester
        self.db_name = db_name
        self.col_name = col_name

    @property
    def _collection(self):
        return self.search_tester.client[self.db_name][self.col_name]

    @staticmethod
    def _classify_error(exc: BaseException) -> tuple[str, Optional[int]]:
        klass = type(exc).__name__
        code: Optional[int] = None
        if isinstance(exc, pymongo.errors.OperationFailure):
            code = exc.code
        return klass, code

    @staticmethod
    def make_cache_buster_query(base_term: str = "movie") -> tuple[dict, str]:
        """Build a ``$search`` stage with a random ``should`` token.

        Mongot's per-query cache is text-keyed; a fresh token forces real
        evaluation without changing which docs match. Returns ``(stage, token)``.
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

    # One-shot mode

    def oneshot_search(
        self,
        query: Optional[dict] = None,
        cache_buster: bool = True,
        limit: int = 10,
        timeout_ms: Optional[int] = None,
        read_preference: Optional[Any] = None,
    ) -> QueryResult:
        """Run a single ``$search`` aggregation.

        ``read_preference`` (e.g. from :func:`cluster_tagged_read_preference`) is applied
        via ``with_options`` so mongos targets the matching members per shard — used to
        prove cluster-locality of ``$search`` routing.
        """
        if query is None:
            if cache_buster:
                stage, _ = self.make_cache_buster_query()
            else:
                stage = {"$search": {"text": {"query": "movie", "path": "plot"}}}
        else:
            stage = query

        pipeline = [stage, {"$limit": limit}, {"$project": {"_id": 0, "title": 1}}]
        kwargs: dict[str, Any] = {}
        if timeout_ms is not None:
            kwargs["maxTimeMS"] = timeout_ms

        collection = self._collection
        if read_preference is not None:
            collection = collection.with_options(read_preference=read_preference)

        started = time.monotonic()
        try:
            docs = list(collection.aggregate(pipeline, **kwargs))
            elapsed_ms = (time.monotonic() - started) * 1000.0
            return QueryResult(success=True, latency_ms=elapsed_ms, returned_count=len(docs))
        except pymongo.errors.PyMongoError as exc:
            elapsed_ms = (time.monotonic() - started) * 1000.0
            klass, code = self._classify_error(exc)
            msg = str(exc)
            return QueryResult(
                success=False,
                latency_ms=elapsed_ms,
                error_class=klass,
                error_code=code,
                error_message=msg,
                failure_class=classify_failure(klass, code, msg),
            )

    def wait_for_sentinel_indexed(
        self,
        *,
        timeout: float = 600.0,
        poll_interval: float = 1.0,
        field_name: str = "plot",
    ) -> str:
        """Insert a unique sentinel doc, poll ``$search`` until mongot returns it.

        A hit proves mongot indexed THIS exact doc and served the query.
        """
        sentinel = f"sentinel_{uuid.uuid4().hex[:16]}"
        self._collection.insert_one({field_name: sentinel, "_sentinel": sentinel})
        pipeline = [
            {"$search": {"text": {"query": sentinel, "path": field_name}}},
            {"$match": {"_sentinel": sentinel}},
            {"$limit": 1},
        ]

        def sentinel_surfaced():
            try:
                hits = list(self._collection.aggregate(pipeline))
                if hits and hits[0].get("_sentinel") == sentinel:
                    return True, "found"
                return False, "no hits yet (mongot still indexing)"
            except pymongo.errors.PyMongoError as exc:
                return False, f"{type(exc).__name__}: {exc}"

        run_periodically(
            sentinel_surfaced,
            timeout=timeout,
            sleep_time=poll_interval,
            msg=f"sentinel {sentinel} to be indexed by mongot",
        )
        return sentinel

    # Paging mode

    @staticmethod
    def _default_paging_stage() -> dict:
        return {
            "$search": {
                "wildcard": {
                    "query": "*",
                    "path": "title",
                    "allowAnalyzedField": True,
                }
            }
        }

    def paging_cursor_open(
        self,
        query: Optional[dict] = None,
        batch_size: int = 10,
        timeout_ms: Optional[int] = None,
    ):
        """Open a ``$search`` aggregation cursor. Caller owns the cursor."""
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
        batch_size: int = 10,
        first_page_index: int = 0,
    ) -> list[QueryResult]:
        """Read ``pages`` pages from an open cursor. No retries — failures recorded as-is."""
        if pages < 1:
            raise ValueError(f"pages must be >= 1; got {pages}")
        results: list[QueryResult] = []
        cursor_alive = True
        for page_offset in range(pages):
            page_index = first_page_index + page_offset
            page_started = time.monotonic()
            docs: list[Any] = []
            page_error: Optional[QueryResult] = None

            for _ in range(batch_size):
                if not cursor_alive:
                    break
                try:
                    docs.append(next(cursor))
                except StopIteration:
                    cursor_alive = False
                    break
                except pymongo.errors.PyMongoError as exc:
                    klass, code = self._classify_error(exc)
                    msg = str(exc)
                    elapsed = (time.monotonic() - page_started) * 1000.0
                    page_error = QueryResult(
                        success=False,
                        latency_ms=elapsed,
                        error_class=klass,
                        error_code=code,
                        error_message=msg,
                        failure_class=classify_failure(klass, code, msg),
                        page_index=page_index,
                    )
                    cursor_alive = False
                    break

            elapsed_ms = (time.monotonic() - page_started) * 1000.0

            if page_error is not None:
                result = page_error
            else:
                result = QueryResult(
                    success=True,
                    latency_ms=elapsed_ms,
                    returned_count=len(docs),
                    page_index=page_index,
                )

            results.append(result)
            if not cursor_alive:
                break
            if interval_seconds > 0 and page_offset + 1 < pages:
                time.sleep(interval_seconds)

        return results

    def paging_search(
        self,
        query: Optional[dict] = None,
        pages: int = 10,
        interval_seconds: float = 1.0,
        batch_size: int = 50,
    ) -> list[QueryResult]:
        """Open a cursor, read ``pages`` pages, close. Open failures land as page 0."""
        try:
            cursor = self.paging_cursor_open(query=query, batch_size=batch_size)
        except pymongo.errors.PyMongoError as exc:
            klass, code = self._classify_error(exc)
            msg = str(exc)
            return [
                QueryResult(
                    success=False,
                    latency_ms=0.0,
                    error_class=klass,
                    error_code=code,
                    error_message=msg,
                    failure_class=classify_failure(klass, code, msg),
                    page_index=0,
                )
            ]
        try:
            return self.paging_cursor_read_pages(
                cursor,
                pages=pages,
                interval_seconds=interval_seconds,
                batch_size=batch_size,
            )
        finally:
            cursor.close()

    # Verdict

    def verdict(self, results: list[QueryResult]) -> ConnectivityVerdict:
        v = ConnectivityVerdict()
        for r in results:
            v.total_operations += 1
            v.total_returned_records += r.returned_count
            if r.success:
                v.succeeded += 1
            else:
                v.failed += 1
                if r.failure_class == FAILURE_CURSOR_LOST:
                    v.cursor_lost += 1
                elif r.failure_class == FAILURE_TRANSIENT_NETWORK:
                    v.transient_network += 1
                elif r.failure_class == FAILURE_INDEX_UNAVAILABLE:
                    v.index_unavailable += 1
                elif r.failure_class == FAILURE_SEARCH_NOT_ENABLED:
                    v.search_not_enabled += 1
                elif r.failure_class == FAILURE_MONGOT_UNREACHABLE:
                    v.mongot_unreachable += 1
                else:
                    v.other_failed += 1
                klass = r.error_class or "Unknown"
                v.error_breakdown[klass] = v.error_breakdown.get(klass, 0) + 1
                msg = r.error_message or klass
                if v.first_error is None:
                    v.first_error = msg
                v.last_error = msg
        return v


# Pod-disruption helpers


def delete_pods(
    namespace: str,
    *,
    name_prefix: Optional[str] = None,
    label_selector: Optional[str] = None,
    grace_period_seconds: Optional[int] = None,
) -> dict[str, str]:
    """Delete matching pods. ``grace_period_seconds=0`` hard-kills; ``None`` uses the pod's own grace.

    Returns ``{pod_name: original_uid}`` for use with ``wait_for_all_pods_replaced``.
    """
    pods = list_matching_pods(namespace, name_prefix=name_prefix, label_selector=label_selector)
    if not pods:
        selector = label_selector or f"name~{name_prefix}*"
        raise AssertionError(f"no pods matching {selector} in ns {namespace}")
    body = (
        client.V1DeleteOptions(grace_period_seconds=grace_period_seconds) if grace_period_seconds is not None else None
    )
    core_v1 = client.CoreV1Api()
    original_uids: dict[str, str] = {}
    for p in pods:
        original_uids[p.metadata.name] = p.metadata.uid
        core_v1.delete_namespaced_pod(name=p.metadata.name, namespace=namespace, body=body)
        logger.info(f"deleted pod {p.metadata.name} (uid={p.metadata.uid[:8]}, grace={grace_period_seconds})")
    return original_uids


def wait_for_all_pods_replaced(
    namespace: str,
    original_uids: dict[str, str],
    *,
    timeout: int = 180,
) -> None:
    """Wait until every named pod's uid changes AND Ready=True.

    Use for StatefulSet pods where replacements keep the same name.
    """
    core_v1 = client.CoreV1Api()

    def check() -> tuple[bool, str]:
        statuses: list[str] = []
        for name, old_uid in original_uids.items():
            try:
                pod = core_v1.read_namespaced_pod(name=name, namespace=namespace)
            except client.exceptions.ApiException as exc:
                if exc.status == 404:
                    statuses.append(f"{name}=terminating")
                    return False, "; ".join(statuses)
                raise
            if pod.metadata.uid == old_uid:
                statuses.append(f"{name}=same_uid")
                return False, "; ".join(statuses)
            ready = pod_is_ready(pod)
            statuses.append(f"{name}=uid_new_ready={ready}")
            if not ready:
                return False, "; ".join(statuses)
        return True, "; ".join(statuses)

    run_periodically(
        check,
        timeout=timeout,
        sleep_time=3,
        msg=f"{len(original_uids)} pods replaced (uid change + Ready)",
    )


def wait_for_pods_by_label_replaced(
    namespace: str,
    label_selector: str,
    original_uids: dict[str, str],
    *,
    api_client: Optional[client.ApiClient] = None,
    expected: Optional[int] = None,
    timeout: int = 180,
) -> None:
    """Wait until label-matching pods have uids NOT in ``original_uids`` AND Ready=True.

    Use for Deployment pods where replacements get fresh names — match by uid set.
    ``api_client`` must target the cluster hosting the pods — for multi-cluster the
    search pods live on the member clusters, not the operator/default cluster.
    """
    want = expected if expected is not None else len(original_uids)
    old_uid_set = set(original_uids.values())

    def check() -> tuple[bool, str]:
        live = [
            p
            for p in list_matching_pods(namespace, label_selector=label_selector, api_client=api_client)
            if p.metadata.deletion_timestamp is None
        ]
        new = [p for p in live if p.metadata.uid not in old_uid_set]
        ready = sum(1 for p in new if pod_is_ready(p))
        return ready >= want, f"new_pods={len(new)} ready={ready}/{want}"

    run_periodically(
        check,
        timeout=timeout,
        sleep_time=3,
        msg=f"{want} new pods with {label_selector} Ready",
    )


# MongoDBSearch reconcile + readiness-probe disruption helpers

DISABLE_RECONCILIATION_ANNOTATION = "mongodb.com/disable-reconciliation"


def set_resource_disabled_annotation(mdbs, disabled: bool) -> None:
    """Toggle ``mongodb.com/disable-reconciliation`` on a MongoDBSearch CR.

    When ``true``, the reconciler stops mutating owned objects so destructive
    test patches survive.
    """
    mdbs.load()
    metadata = mdbs["metadata"]
    annotations = metadata.get("annotations") or {}
    # update() is a JSON merge patch: deleting a key requires an explicit None
    # (popping it is a no-op and would leave reconciliation disabled forever).
    annotations[DISABLE_RECONCILIATION_ANNOTATION] = "true" if disabled else None
    metadata["annotations"] = annotations
    mdbs["metadata"] = metadata
    mdbs.update()
    logger.info(f"{DISABLE_RECONCILIATION_ANNOTATION}={'true' if disabled else '<unset>'} on MongoDBSearch {mdbs.name}")


def patch_mongot_readiness_probe_to_false(namespace: str, sts_name: str, container_name: str = "mongot") -> None:
    """Patch ``container_name``'s readiness probe to ``/bin/false`` on the STS pod
    TEMPLATE. A template patch ROLLS an already-running pod (the process restarts);
    the replacement pod runs with the failing probe and never reports Ready. Wait
    for the rolled pod to be Running-but-not-ready before relying on the hold."""
    patch = {
        "spec": {
            "template": {
                "spec": {
                    "containers": [
                        {
                            "name": container_name,
                            "readinessProbe": {
                                "exec": {"command": ["/bin/false"]},
                                "httpGet": None,
                                "tcpSocket": None,
                                "grpc": None,
                                "periodSeconds": 2,
                                "failureThreshold": 1,
                                "timeoutSeconds": 1,
                                "initialDelaySeconds": 0,
                            },
                        }
                    ]
                }
            }
        }
    }
    client.AppsV1Api().patch_namespaced_stateful_set(name=sts_name, namespace=namespace, body=patch)
    logger.info(f"patched {sts_name} container={container_name} readinessProbe -> /bin/false")


def mongot_readiness_is_forced_false(namespace: str, sts_name: str, container_name: str = "mongot") -> bool:
    """True iff ``container_name``'s readiness probe on the STS template is the
    forced-fail ``/bin/false`` exec from patch_mongot_readiness_probe_to_false.
    Lets callers detect a reconcile that reverted the patch and re-apply it."""
    sts = client.AppsV1Api().read_namespaced_stateful_set(sts_name, namespace)
    for c in sts.spec.template.spec.containers:
        if c.name == container_name:
            probe = c.readiness_probe
            return bool(probe and probe._exec and probe._exec.command == ["/bin/false"])
    return False


def restore_mongot_readiness_probe(namespace: str, sts_name: str, container_name: str = "mongot") -> None:
    """Clear the test readiness-probe override and delete the pod so it picks up the
    change — a probe-only template edit doesn't roll a stuck not-ready pod."""
    patch = {"spec": {"template": {"spec": {"containers": [{"name": container_name, "readinessProbe": None}]}}}}
    client.AppsV1Api().patch_namespaced_stateful_set(name=sts_name, namespace=namespace, body=patch)
    core = client.CoreV1Api()
    for pod in list_matching_pods(namespace, name_prefix=sts_name):
        core.delete_namespaced_pod(pod.metadata.name, namespace)
        logger.info(f"deleted pod {pod.metadata.name} to roll the restored readiness probe")
    logger.info(f"cleared {sts_name} container={container_name} readinessProbe override")


# Post-fault drain budget — mongot's gRPC reply to mongod is server-
# streaming; mongod buffers an unknown depth, depending on the number
# of actual getMore executed against mongot
DEFAULT_POST_FAULT_DRAIN_FLOOR = 50_000


def paging_baseline_and_fault(
    tool: "SearchConnectivityTool",
    *,
    baseline_pages: int = 2,
    max_post_fault_pages: int = 5_000,
    min_post_fault_docs: int = DEFAULT_POST_FAULT_DRAIN_FLOOR,
    baseline_interval_seconds: float = 0.1,
    post_fault_interval_seconds: float = 0.05,
    post_fault_pages_read: int = 10,
    batch_size: int = 10,
    baseline_batch_size: int = 10,
    fault_fn: Callable[[], None],
) -> tuple[list[QueryResult], list[QueryResult], "ConnectivityVerdict"]:
    """Open paging cursor → baseline → ``fault_fn()`` → drain post-fault.

    Returns ``(baseline, post, verdict_over_post)``. Drains until a failure
    surfaces OR ``min_post_fault_docs`` is reached — we force the buffer to
    drain so the fault becomes observable.
    """
    cursor = tool.paging_cursor_open(batch_size=baseline_batch_size)
    try:
        baseline = tool.paging_cursor_read_pages(
            cursor,
            pages=baseline_pages,
            interval_seconds=baseline_interval_seconds,
            batch_size=baseline_batch_size,
            first_page_index=0,
        )
        logger.info("baseline pages: %s", "; ".join(str(p) for p in baseline))
        if not any(p.success and p.returned_count > 0 for p in baseline):
            raise AssertionError(f"baseline returned no docs; cannot exercise fault: {baseline}")
        fault_fn()
        logger.info("fault executed")

        post: list[QueryResult] = []
        post_docs = 0
        saw_failure = False
        while len(post) < max_post_fault_pages:
            batch = tool.paging_cursor_read_pages(
                cursor,
                pages=post_fault_pages_read,
                interval_seconds=post_fault_interval_seconds,
                batch_size=batch_size,
                first_page_index=len(baseline) + len(post),
            )
            post.extend(batch)
            for p in batch:
                post_docs += p.returned_count
                if not p.success:
                    saw_failure = True
            if saw_failure or post_docs >= min_post_fault_docs:
                break
        logger.info(
            "post-fault pages (count=%d docs=%d): %s",
            len(post),
            post_docs,
            "; ".join(str(p) for p in post),
        )
        verdict = tool.verdict(post)
        logger.info(f"post-fault verdict: {verdict.as_dict()}")
        if not saw_failure and post_docs < min_post_fault_docs:
            raise AssertionError(
                f"post-fault drain stopped at {post_docs} docs across {len(post)} pages "
                f"without crossing the {min_post_fault_docs}-doc buffer ceiling and "
                f"without surfacing a failure — the fault path may not have been exercised."
            )
        return baseline, post, verdict
    finally:
        cursor.close()


CLUSTER_LOCATION_TAG_KEY = "nodeLocation"


def cluster_tagged_read_preference(cluster_name: str, *, tag_key: str = CLUSTER_LOCATION_TAG_KEY) -> Nearest:
    """A ``nearest`` read preference pinned to RS members tagged with this cluster.

    mongos honours read-preference tagSets when targeting each shard's node, so this
    forces a same-cluster member per shard (and therefore that member's same-cluster
    mongot). ``nearest`` is required because ``primary`` mode ignores tagSets. Pairs
    with member tags set via the deployment's ``memberConfig[].tags``.
    """
    return Nearest(tag_sets=[{tag_key: cluster_name}])


def scale_mongot_statefulset(
    sts_name: str,
    namespace: str,
    replicas: int,
    *,
    api_client: Optional[client.ApiClient] = None,
) -> None:
    """Set ONE mongot StatefulSet's replica count directly via the scale subresource.

    Finer-grained than the operator's per-cluster ``spec.clusters[].replicas``: this
    takes a single shard's mongot offline in a single cluster, so a oneshot ``$search``
    fan-out hits a shard whose mongot is gone. Pair with
    ``set_resource_disabled_annotation`` so the operator doesn't reconcile the replica
    count back. ``api_client`` must target the cluster hosting the STS (mongot
    StatefulSets live on the member clusters, not the operator/default cluster).
    """
    client.AppsV1Api(api_client=api_client).patch_namespaced_stateful_set_scale(
        name=sts_name,
        namespace=namespace,
        body={"spec": {"replicas": replicas}},
    )
    logger.info(f"scaled mongot StatefulSet {sts_name} -> replicas={replicas}")


def delete_mongot_pvcs(
    namespace: str,
    sts_name: str,
    *,
    api_client: Optional[client.ApiClient] = None,
) -> list[str]:
    """Delete the PVCs backing a mongot StatefulSet so a recreated mongot starts
    on a clean volume.

    A reused volume carries the old index catalog; on restart mongot can latch its
    one-way ``/ready`` state on stale indexes (or loop initial-sync on a dead
    collectionUUID). Call this only when the STS is gone or scaled to 0 — otherwise
    the STS controller immediately recreates the claim. PVCs from a
    volumeClaimTemplate are named ``<template>-<sts>-<ordinal>``, so we match on the
    ``-<sts>-`` infix. Returns the deleted PVC names; missing PVCs are a no-op.
    """
    # Enforce the docstring's precondition: a live STS instantly recreates/holds claims.
    try:
        sts = client.AppsV1Api(api_client=api_client).read_namespaced_stateful_set(sts_name, namespace)
        desired = (sts.spec.replicas if sts.spec else 0) or 0
        if desired > 0:
            raise AssertionError(f"delete_mongot_pvcs called while STS {sts_name} has replicas={desired}")
    except client.exceptions.ApiException as exc:
        if exc.status != 404:
            raise
    core = client.CoreV1Api(api_client=api_client)
    pvcs = core.list_namespaced_persistent_volume_claim(namespace).items
    deleted: list[str] = []
    for pvc in pvcs:
        if f"-{sts_name}-" in f"-{pvc.metadata.name}":
            core.delete_namespaced_persistent_volume_claim(pvc.metadata.name, namespace)
            deleted.append(pvc.metadata.name)
            logger.info(f"deleted PVC {pvc.metadata.name} for mongot STS {sts_name}")
    if not deleted:
        logger.info(f"no PVCs matched mongot STS {sts_name} in ns {namespace}")
    return deleted


def wait_for_mongot_statefulset_drained(
    sts_name: str,
    namespace: str,
    *,
    api_client: Optional[client.ApiClient] = None,
    timeout: int = 180,
    sleep_time: int = 5,
) -> None:
    """Wait until the mongot StatefulSet has 0 replicas or is deleted.

    On ``spec.clusters[].replicas`` -> 0 the operator keeps the StatefulSet but
    scales it to 0, so ``replicas == ready == desired == 0`` is the normal
    terminal state. ``status.replicas`` (not just ``readyReplicas``) must reach 0:
    a Terminating pod is unready but still counted, and only its full deletion
    means the drain completed (the PVC GC under ``WhenScaled: Delete`` starts
    after that — see ``wait_for_mongot_pvcs_deleted``). A 404 is also accepted
    for callers that remove the STS outright (e.g. CR delete). ``api_client``
    must target the cluster that hosts the STS — for multi-cluster the mongot
    StatefulSets live on the member clusters, not the operator/default cluster.
    """
    apps_v1 = client.AppsV1Api(api_client=api_client)

    def drained() -> tuple[bool, str]:
        try:
            sts = apps_v1.read_namespaced_stateful_set(sts_name, namespace)
        except client.exceptions.ApiException as exc:
            if exc.status == 404:
                return True, f"{sts_name} deleted by reconciler"
            raise
        replicas = sts.status.replicas or 0
        ready = sts.status.ready_replicas or 0
        desired = (sts.spec.replicas if sts.spec else 0) or 0
        return (
            replicas == 0 and ready == 0 and desired == 0,
            f"replicas={replicas}, ready={ready}, desired={desired}",
        )

    run_periodically(
        drained,
        timeout=timeout,
        sleep_time=sleep_time,
        msg=f"mongot StatefulSet {sts_name} to drain",
    )


def wait_for_mongot_pvcs_deleted(
    namespace: str,
    sts_name: str,
    *,
    api_client: Optional[client.ApiClient] = None,
    timeout: int = 300,
    sleep_time: int = 5,
) -> None:
    """Wait until the PVCs backing a mongot StatefulSet are fully deleted.

    Under ``WhenScaled: Delete`` the STS controller GCs the per-ordinal claim on
    scale-to-0, but the delete is asynchronous. Scaling back up while the old
    claim is still ``Terminating`` trips a StatefulSet-controller race: the
    controller skips pod creation for the deleting claim and is not re-enqueued
    once it is gone, leaving the pod stuck at ``current: 0``. Block on the claim
    being gone before restoring replicas. PVCs from a volumeClaimTemplate are
    named ``<template>-<sts>-<ordinal>``, so we match on the ``-<sts>-`` infix.
    ``api_client`` must target the cluster hosting the STS.
    """

    def deleted() -> tuple[bool, str]:
        remaining = mongot_data_pvc_names(namespace, sts_name, api_client=api_client)
        return not remaining, ("all deleted" if not remaining else f"remaining={remaining}")

    run_periodically(
        deleted,
        timeout=timeout,
        sleep_time=sleep_time,
        msg=f"mongot PVCs for STS {sts_name} to be deleted",
    )


def _search_owned_top_level_resources(
    apps_v1: Any,
    core_v1: Any,
    namespace: str,
    search_name: str,
) -> dict[str, list[Any]]:
    selector = f"{SEARCH_OWNER_NAME_LABEL}={search_name},{SEARCH_OWNER_NAMESPACE_LABEL}={namespace}"
    return {
        "StatefulSet": apps_v1.list_namespaced_stateful_set(namespace, label_selector=selector).items,
        "Deployment": apps_v1.list_namespaced_deployment(namespace, label_selector=selector).items,
        "Service": core_v1.list_namespaced_service(namespace, label_selector=selector).items,
        "ConfigMap": core_v1.list_namespaced_config_map(namespace, label_selector=selector).items,
        "Secret": core_v1.list_namespaced_secret(namespace, label_selector=selector).items,
    }


def wait_for_search_owned_resources_deleted(
    apps_v1: Any,
    core_v1: Any,
    namespace: str,
    search_name: str,
    *,
    where: str,
    timeout: int = 600,
) -> None:
    def deleted() -> tuple[bool, str]:
        resources = _search_owned_top_level_resources(apps_v1, core_v1, namespace, search_name)
        remaining = [
            f"{kind}/{resource.metadata.name}(uid={resource.metadata.uid})"
            for kind, items in resources.items()
            for resource in items
        ]
        return not remaining, f"{where}: remaining={remaining}"

    run_periodically(
        deleted,
        timeout=timeout,
        sleep_time=5,
        msg=f"{where} Search-owned top-level resource cleanup",
    )


def mongot_data_pvc_names(
    namespace: str,
    sts_name: str,
    *,
    api_client: Optional[client.ApiClient] = None,
) -> list[str]:
    core = client.CoreV1Api(api_client=api_client)
    return [
        pvc.metadata.name
        for pvc in core.list_namespaced_persistent_volume_claim(namespace).items
        if f"-{sts_name}-" in f"-{pvc.metadata.name}"
    ]


def wait_for_resource_deleted(read_fn: Callable[[], Any], what: str, timeout: int = 300) -> None:
    def deleted() -> tuple[bool, str]:
        try:
            read_fn()
            return False, f"{what} still present"
        except client.exceptions.ApiException as exc:
            if exc.status == 404:
                return True, f"{what} deleted"
            raise

    run_periodically(deleted, timeout=timeout, sleep_time=5, msg=f"{what} cleanup")


def wait_for_search_deleted(mdbs: "MongoDBSearch", timeout: int = 300) -> None:
    wait_for_resource_deleted(mdbs.load, f"MongoDBSearch {mdbs.name}", timeout=timeout)


def protected_search_input_uids(
    core_v1: Any,
    namespace: str,
    source_tls_secret_name: str,
    sync_user_secret_name: str,
    ca_configmap_name: str,
    *,
    additional_secret_names: tuple[str, ...] = (),
) -> dict[str, str]:
    def uid(resource: Any, what: str) -> str:
        value = resource.metadata.uid
        assert value, f"{what} has no UID"
        return value

    uids = {
        "source_tls_secret": uid(
            core_v1.read_namespaced_secret(source_tls_secret_name, namespace),
            f"Secret {source_tls_secret_name}",
        ),
        "sync_user_secret": uid(
            core_v1.read_namespaced_secret(sync_user_secret_name, namespace),
            f"Secret {sync_user_secret_name}",
        ),
        "ca_configmap": uid(
            core_v1.read_namespaced_config_map(ca_configmap_name, namespace),
            f"ConfigMap {ca_configmap_name}",
        ),
    }
    uids.update(
        {
            f"secret/{name}": uid(core_v1.read_namespaced_secret(name, namespace), f"Secret {name}")
            for name in additional_secret_names
        }
    )
    return uids


def search_artifact_uids(
    mcc: "MultiClusterClient",
    namespace: str,
    names: dict[str, str],
) -> dict[str, str]:
    apps = mcc.apps_v1_api()
    core = mcc.core_v1_api()
    resources = {
        f"StatefulSet/{names['sts']}": mcc.read_namespaced_stateful_set(names["sts"], namespace),
        f"Service/{names['svc']}": mcc.read_namespaced_service(names["svc"], namespace),
        f"Service/{names['proxy']}": mcc.read_namespaced_service(names["proxy"], namespace),
        f"ConfigMap/{names['mongot_cm']}": mcc.read_namespaced_config_map(names["mongot_cm"], namespace),
        f"Deployment/{names['envoy_deployment']}": apps.read_namespaced_deployment(
            names["envoy_deployment"], namespace
        ),
        f"ConfigMap/{names['envoy_cm']}": mcc.read_namespaced_config_map(names["envoy_cm"], namespace),
        f"Secret/{names['operator_tls_secret']}": core.read_namespaced_secret(names["operator_tls_secret"], namespace),
    }
    pvc_names = mongot_data_pvc_names(namespace, names["sts"], api_client=mcc.api_client)
    assert pvc_names, f"[{mcc.cluster_name}] expected mongot data PVCs for {names['sts']}"
    resources.update(
        {
            f"PersistentVolumeClaim/{name}": core.read_namespaced_persistent_volume_claim(name, namespace)
            for name in pvc_names
        }
    )

    uids = {what: resource.metadata.uid for what, resource in resources.items()}
    assert all(uids.values()), f"[{mcc.cluster_name}] Search artifact without UID: {uids}"
    return uids


def wait_for_search_artifacts_deleted(
    mcc: "MultiClusterClient",
    namespace: str,
    sts_name: str,
    search_name: str,
    *,
    timeout: int = 600,
) -> None:
    """Label-scoped absence is the authoritative cleanup assertion; the exact-name
    STS check on top catches accidental label stripping on the primary artifact."""
    wait_for_resource_deleted(
        lambda: mcc.read_namespaced_stateful_set(sts_name, namespace),
        f"STS {sts_name} in {mcc.cluster_name}",
        timeout=timeout,
    )
    wait_for_mongot_pvcs_deleted(namespace, sts_name, api_client=mcc.api_client, timeout=300)
    wait_for_search_owned_resources_deleted(
        mcc.apps_v1_api(),
        mcc.core_v1_api(),
        namespace,
        search_name,
        where=mcc.cluster_name,
        timeout=timeout,
    )


def hard_kill_pods_by_label(
    core_v1: Any,
    namespace: str,
    label_key: str,
    label_value: str,
) -> dict[str, str]:
    """Hard-kill (grace=0) all pods matching ``label_key=label_value``.

    Returns ``{pod_name: uid}`` of killed pods.
    """
    selector = f"{label_key}={label_value}"
    pods = core_v1.list_namespaced_pod(namespace=namespace, label_selector=selector).items
    if not pods:
        raise AssertionError(f"no pods matched {selector} in ns {namespace}")
    uids: dict[str, str] = {}
    body = client.V1DeleteOptions(grace_period_seconds=0)
    for p in pods:
        uids[p.metadata.name] = p.metadata.uid
        try:
            core_v1.delete_namespaced_pod(name=p.metadata.name, namespace=namespace, body=body)
            logger.info(f"hard-killed pod {p.metadata.name} (uid={p.metadata.uid[:8]})")
        except client.exceptions.ApiException as exc:
            if exc.status == 404:
                logger.info(f"pod {p.metadata.name} already gone (404), skipping")
            else:
                raise
    return uids


def assert_disruption_observed(
    verdict: "ConnectivityVerdict",
    context: str = "",
) -> None:
    """Assert that the connectivity verdict shows at least one disruption.

    Accepts ``cursor_lost`` or ``transient_network`` as evidence.
    """
    ctx = f"[{context}] " if context else ""
    if verdict.total_operations == 0:
        raise AssertionError(f"{ctx}verdict has no operations — the harness never ran. verdict={verdict.as_dict()}")
    if verdict.cursor_lost == 0 and verdict.transient_network == 0:
        raise AssertionError(
            f"{ctx}no disruption observed: cursor_lost=0 transient_network=0; verdict={verdict.as_dict()}"
        )


def assert_no_index_unavailable(verdict: "ConnectivityVerdict", context: str = "") -> None:
    """Fail if any probe got a mongot index-state rejection (INITIAL_SYNC etc.).

    That would mean a Ready-latched mongot served a still-syncing range — the
    onboarding mode the operator can't yet route around. The only acceptable
    onboarding-gap failure is "no mongot reachable" (transient_network /
    search_not_enabled), never this.
    """
    ctx = f"[{context}] " if context else ""
    if verdict.index_unavailable > 0:
        raise AssertionError(
            f"{ctx}{verdict.index_unavailable} probe(s) hit a mongot index-state rejection "
            f"(INITIAL_SYNC/NOT_STARTED) instead of a clean no-upstream gap; verdict={verdict.as_dict()}"
        )


def assert_clean_no_mongot_gap(verdict: "ConnectivityVerdict", context: str = "") -> None:
    """Assert the onboarding gap is the clean "no mongot reachable" failure.

    The contract: at least one probe failed with the clean no-mongot signal
    (transient_network "no healthy upstream", search_not_enabled "mongotHost
    absent", or mongot_unreachable "mongotHost set but nothing answers"), and
    *no* probe got a mongot index-state rejection. Incidental ``other`` failures
    are tolerated but logged — the hard invariants are "saw the clean gap" and
    "never saw INITIAL_SYNC".
    """
    ctx = f"[{context}] " if context else ""
    if verdict.failed == 0:
        raise AssertionError(f"{ctx}expected the onboarding gap to fail some probes; verdict={verdict.as_dict()}")
    assert_no_index_unavailable(verdict, context)
    clean = verdict.transient_network + verdict.search_not_enabled + verdict.mongot_unreachable
    if clean == 0:
        raise AssertionError(
            f"{ctx}gap failed but with no clean no-mongot signal "
            f"(transient_network/search_not_enabled/mongot_unreachable all 0); verdict={verdict.as_dict()}"
        )
    if clean < verdict.failed:
        logger.warning(f"{ctx}{verdict.failed - clean} incidental non-gap failure(s); verdict={verdict.as_dict()}")
