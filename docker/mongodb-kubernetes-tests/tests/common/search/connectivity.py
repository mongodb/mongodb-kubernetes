"""Search connectivity tool for MongoDBSearch availability testing.

Issues ``$search`` queries against an MCK-deployed cluster and returns
structured per-query results. Two modes: one-shot and long-running paging.
"""

from __future__ import annotations

import re
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, Callable, Optional

import pymongo
import pymongo.errors
from kubernetes import client
from kubetester import list_matching_pods, pod_is_ready
from kubetester.kubetester import run_periodically
from tests import test_logger
from tests.common.search.search_tester import SearchTester

logger = test_logger.get_test_logger(__name__)


FAILURE_CURSOR_LOST = "cursor_lost"
FAILURE_TRANSIENT_NETWORK = "transient_network"
FAILURE_OTHER = "other"

_CURSOR_LOST_MESSAGE_RE = re.compile(
    r"cursor id .*?(not found|was killed)|remote error from mongot|rst_stream",
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
    """Map ``(class, code, message)`` to one of cursor_lost / transient_network / other.

    cursor_lost takes precedence: "Remote error from mongot" during a pod
    restart is unrecoverable on the same cursor even if mongot comes back.
    """
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

    total: int = 0
    succeeded: int = 0
    failed: int = 0
    cursor_lost: int = 0
    transient_network: int = 0
    other_failed: int = 0
    error_breakdown: dict[str, int] = field(default_factory=dict)
    first_error: Optional[str] = None
    last_error: Optional[str] = None

    def as_dict(self) -> dict[str, Any]:
        return {
            "total": self.total,
            "succeeded": self.succeeded,
            "failed": self.failed,
            "cursor_lost": self.cursor_lost,
            "transient_network": self.transient_network,
            "other_failed": self.other_failed,
            "error_breakdown": dict(self.error_breakdown),
            "first_error": self.first_error,
            "last_error": self.last_error,
        }


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
    ) -> QueryResult:
        """Run a single ``$search`` aggregation."""
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

        started = time.monotonic()
        try:
            docs = list(self._collection.aggregate(pipeline, **kwargs))
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
            v.total += 1
            if r.success:
                v.succeeded += 1
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
    expected: Optional[int] = None,
    timeout: int = 180,
) -> None:
    """Wait until label-matching pods have uids NOT in ``original_uids`` AND Ready=True.

    Use for Deployment pods where replacements get fresh names — match by uid set.
    """
    want = expected if expected is not None else len(original_uids)
    old_uid_set = set(original_uids.values())

    def check() -> tuple[bool, str]:
        live = [
            p
            for p in list_matching_pods(namespace, label_selector=label_selector)
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

RESOURCE_DISABLED_ANNOTATION = "mongodb.com/resourceDisabled"


def set_resource_disabled_annotation(mdbs, disabled: bool) -> None:
    """Toggle ``mongodb.com/resourceDisabled`` on a MongoDBSearch CR.

    When ``true``, the reconciler stops mutating owned objects so destructive
    test patches survive.
    """
    mdbs.load()
    metadata = mdbs["metadata"]
    annotations = metadata.get("annotations") or {}
    if disabled:
        annotations[RESOURCE_DISABLED_ANNOTATION] = "true"
    else:
        annotations.pop(RESOURCE_DISABLED_ANNOTATION, None)
    metadata["annotations"] = annotations
    mdbs["metadata"] = metadata
    mdbs.update()
    logger.info(f"{RESOURCE_DISABLED_ANNOTATION}={'true' if disabled else '<unset>'} on MongoDBSearch {mdbs.name}")


def patch_mongot_readiness_probe_to_false(namespace: str, sts_name: str, container_name: str = "mongot") -> None:
    """Patch ``container_name``'s readiness probe to ``/bin/false``."""
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


def restore_mongot_readiness_probe(namespace: str, sts_name: str, container_name: str = "mongot") -> None:
    """Clear the test-injected readiness probe override."""
    patch = {"spec": {"template": {"spec": {"containers": [{"name": container_name, "readinessProbe": None}]}}}}
    client.AppsV1Api().patch_namespaced_stateful_set(name=sts_name, namespace=namespace, body=patch)
    logger.info(f"cleared {sts_name} container={container_name} readinessProbe override")


# Post-fault drain budget — mongot's gRPC reply to mongod is server-
# streaming; mongod buffers an unknown depth on top. The pod-restart fault
# is only observable AFTER the caller drains more docs than the server-
# stream + buffer can supply. RS pod-restart empirically required >= 50k.
DEFAULT_POST_FAULT_DRAIN_FLOOR = 50_000


def paging_baseline_and_fault(
    tool: "SearchConnectivityTool",
    *,
    baseline_pages: int = 2,
    max_post_fault_pages: int = 5_000,
    min_post_fault_docs: int = DEFAULT_POST_FAULT_DRAIN_FLOOR,
    baseline_interval_seconds: float = 0.1,
    post_fault_interval_seconds: float = 0.05,
    batch_size: int = 10,
    fault_fn: Callable[[], None],
) -> tuple[list[QueryResult], list[QueryResult], "ConnectivityVerdict"]:
    """Open paging cursor → baseline → ``fault_fn()`` → drain post-fault.

    Returns ``(baseline, post, verdict_over_post)``. Drains until a failure
    surfaces OR ``min_post_fault_docs`` is reached — we force the buffer to
    drain so the fault becomes observable.
    """
    cursor = tool.paging_cursor_open(batch_size=batch_size)
    try:
        baseline = tool.paging_cursor_read_pages(
            cursor,
            pages=baseline_pages,
            interval_seconds=baseline_interval_seconds,
            batch_size=batch_size,
            first_page_index=0,
        )
        logger.info("baseline pages: %s", "; ".join(str(p) for p in baseline))
        if not any(p.success and p.returned_count > 0 for p in baseline):
            raise AssertionError(f"baseline returned no docs; cannot exercise fault: {baseline}")
        fault_fn()

        post: list[QueryResult] = []
        post_docs = 0
        saw_failure = False
        while len(post) < max_post_fault_pages:
            batch = tool.paging_cursor_read_pages(
                cursor,
                pages=1,
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


def wait_for_mongot_statefulset_drained(
    sts_name: str,
    namespace: str,
    *,
    timeout: int = 180,
    sleep_time: int = 5,
) -> None:
    """Wait until the mongot StatefulSet has 0 ready replicas or is deleted.

    The reconciler deletes the STS entirely when ``spec.replicas`` drops to 0,
    so a 404 from the API is the terminal state.
    """
    apps_v1 = client.AppsV1Api()

    def drained() -> tuple[bool, str]:
        try:
            sts = apps_v1.read_namespaced_stateful_set(sts_name, namespace)
        except client.exceptions.ApiException as exc:
            if exc.status == 404:
                return True, f"{sts_name} deleted by reconciler"
            raise
        ready = sts.status.ready_replicas or 0
        desired = (sts.spec.replicas if sts.spec else 0) or 0
        return ready == 0 and desired == 0, f"ready={ready}, desired={desired}"

    run_periodically(
        drained,
        timeout=timeout,
        sleep_time=sleep_time,
        msg=f"mongot StatefulSet {sts_name} to drain",
    )
