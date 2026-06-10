"""Stream-level tracing for the managed Envoy LB, from its JSON logs + admin /stats.

Envoy emits one JSON access-log line per HTTP/2 stream close (buildHCMAccessLog in
envoy_config_builder.go) carrying the MONGODB-CLIENTID, the picked mongot UPSTREAM_HOST,
the RESPONSE_FLAGS disposition, and the gRPC status. Component logs share the same
top-level shape but with logger != "access". This module reads the envoy pod's stdout
via the k8s API, parses the access records, and reads the admin /stats counters through
the apiserver pod-proxy (no curl in the envoy image, /stats is on the admin allow-list).

Pairs with connectivity.py / background_availability_tester.py: those measure
client-observed availability; this correlates it to the envoy-side stream disposition so a
drain can be asserted at the stream level (in-flight completion + new-downstream re-route).
"""

from __future__ import annotations

import datetime
import json
import threading
from dataclasses import dataclass
from typing import Optional

from kubernetes import client
from kubetester import list_matching_pods
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

ENVOY_CONTAINER = "envoy"
ENVOY_ADMIN_PORT = 9901  # EnvoyAdminPort in mongodbsearchenvoy_controller.go

# RESPONSE_FLAGS that mark a forced / abnormal stream close (vs a clean "-").
# DC downstream-conn termination, UC upstream-conn termination, UF upstream-conn
# failure, UR upstream remote reset, URX upstream retry-limit, DPE downstream
# protocol error. A graceful GOAWAY drain lets in-flight streams finish "-" (clean).
FORCED_CLOSE_FLAGS = frozenset({"DC", "UC", "UF", "UR", "URX", "DPE", "LR", "UT"})

_EMPTY = "-"  # envoy renders an absent substitution field as "-"


def _clean(value: Optional[str]) -> Optional[str]:
    """Normalise envoy's "-" placeholder (and empty string) to None."""
    if value is None or value == _EMPTY or value == "":
        return None
    return value


def _parse_ts(raw: Optional[str]) -> Optional[datetime.datetime]:
    """Parse the access-log START_TIME (ISO 8601 with ms + ±HH:MM offset)."""
    raw = _clean(raw)
    if raw is None:
        return None
    try:
        return datetime.datetime.fromisoformat(raw)
    except ValueError:
        return None


@dataclass
class StreamRecord:
    """One envoy access-log line — a single closed HTTP/2 (gRPC) stream."""

    client_id: Optional[str]
    upstream_host: Optional[str]
    response_flags: Optional[str]
    grpc_status: Optional[str]
    response_code: Optional[str]
    path: Optional[str]
    duration_ms: Optional[float]
    ts: Optional[datetime.datetime]
    raw: dict

    @classmethod
    def from_json(cls, obj: dict) -> "StreamRecord":
        dur = _clean(obj.get("duration_ms"))
        try:
            duration_ms = float(dur) if dur is not None else None
        except (TypeError, ValueError):
            duration_ms = None
        return cls(
            client_id=_clean(obj.get("client_id")),
            upstream_host=_clean(obj.get("upstream_host")),
            response_flags=_clean(obj.get("response_flags")),
            grpc_status=_clean(obj.get("grpc_status")),
            response_code=_clean(obj.get("response_code")),
            path=_clean(obj.get("path")),
            duration_ms=duration_ms,
            ts=_parse_ts(obj.get("time")),
            raw=obj,
        )

    @property
    def forced_closed(self) -> bool:
        """True if RESPONSE_FLAGS carries any forced/abnormal close flag."""
        if not self.response_flags:
            return False
        return any(flag in FORCED_CLOSE_FLAGS for flag in self.response_flags.split(","))


def _envoy_pod_names(namespace: str, pod_or_selector: str) -> list[str]:
    """Resolve a single pod name, or every pod matching a ``key=value`` selector."""
    if "=" in pod_or_selector:
        return [p.metadata.name for p in list_matching_pods(namespace, label_selector=pod_or_selector)]
    return [pod_or_selector]


def _parse_access_line(line: str) -> Optional[StreamRecord]:
    """Parse one stdout line into a StreamRecord; None for non-JSON / non-access lines."""
    line = line.strip()
    if not line or line[0] != "{":
        return None
    try:
        obj = json.loads(line)
    except json.JSONDecodeError:
        return None
    if obj.get("logger") != "access":
        return None
    return StreamRecord.from_json(obj)


def _by_ts(record: StreamRecord) -> datetime.datetime:
    return record.ts or datetime.datetime.min.replace(tzinfo=datetime.timezone.utc)


def read_envoy_logs(
    namespace: str,
    pod_or_selector: str,
    *,
    api_client: Optional[client.ApiClient] = None,
    since_seconds: Optional[int] = None,
) -> list[StreamRecord]:
    """Read the access-log StreamRecords from one envoy pod or a label selector.

    Component-log lines (logger != "access") and non-JSON lines are skipped. Pod logs
    only cover the *current* container instance — read before a roll, or read the
    replacement pods after, accordingly.
    """
    core_v1 = client.CoreV1Api(api_client=api_client)
    records: list[StreamRecord] = []
    for pod in _envoy_pod_names(namespace, pod_or_selector):
        kwargs: dict = {"name": pod, "namespace": namespace, "container": ENVOY_CONTAINER}
        if since_seconds is not None:
            kwargs["since_seconds"] = since_seconds
        try:
            raw_log = core_v1.read_namespaced_pod_log(**kwargs)
        except client.exceptions.ApiException as exc:
            logger.warning(f"could not read logs for envoy pod {pod}: {exc}")
            continue
        for line in raw_log.splitlines():
            record = _parse_access_line(line)
            if record is not None:
                records.append(record)
    records.sort(key=_by_ts)
    return records


class EnvoyLogFollower:
    """Follows envoy pods' logs in daemon threads so records from pods that terminate mid-roll
    survive (an after-the-fact ``read_envoy_logs`` only sees replacements). start() before the
    disruption, stop() after — the bounded join means a stuck stream can't hang the test."""

    def __init__(
        self,
        namespace: str,
        pod_or_selector: str,
        *,
        api_client: Optional[client.ApiClient] = None,
    ):
        self._namespace = namespace
        self._api_client = api_client
        self._pods = _envoy_pod_names(namespace, pod_or_selector)
        self._records: list[StreamRecord] = []
        self._lock = threading.Lock()
        self._threads: list[threading.Thread] = []

    def start(self) -> "EnvoyLogFollower":
        for pod in self._pods:
            thread = threading.Thread(target=self._follow, args=(pod,), name=f"envoy-log-follow-{pod}", daemon=True)
            thread.start()
            self._threads.append(thread)
        logger.info(f"following envoy logs of {self._pods}")
        return self

    def _follow(self, pod: str) -> None:
        core_v1 = client.CoreV1Api(api_client=self._api_client)
        try:
            resp = core_v1.read_namespaced_pod_log(
                name=pod,
                namespace=self._namespace,
                container=ENVOY_CONTAINER,
                follow=True,
                _preload_content=False,
            )
            for raw_line in resp:
                record = _parse_access_line(raw_line.decode("utf-8", errors="replace"))
                if record is not None:
                    with self._lock:
                        self._records.append(record)
        except Exception as exc:  # the followed pod terminating mid-stream is the expected end
            logger.info(f"log follow ended for envoy pod {pod}: {exc}")

    def stop(self, timeout_seconds: float = 30.0) -> list[StreamRecord]:
        """Join the follower threads and return the accumulated records, ts-sorted."""
        for thread in self._threads:
            thread.join(timeout=timeout_seconds)
        with self._lock:
            records = list(self._records)
        records.sort(key=_by_ts)
        return records


# Query helpers the scenarios use --------------------------------------------


def streams_active_between(
    records: list[StreamRecord],
    t0: datetime.datetime,
    t1: datetime.datetime,
) -> list[StreamRecord]:
    """Records whose stream closed within [t0, t1] (access log emits at close)."""
    return [r for r in records if r.ts is not None and t0 <= r.ts <= t1]


def forced_closed(records: list[StreamRecord]) -> list[StreamRecord]:
    """Records that closed with a forced/abnormal RESPONSE_FLAGS disposition."""
    return [r for r in records if r.forced_closed]


def new_downstream_after(
    records: list[StreamRecord],
    t0: datetime.datetime,
) -> list[StreamRecord]:
    """Streams after t0 whose client-id was not seen before t0.

    A fresh MONGODB-CLIENTID closing after the drain is the signal that mongod opened a
    NEW downstream connection (rather than riding the drained one) — the mongod-honor side.
    """
    seen_before = {r.client_id for r in records if r.client_id and r.ts is not None and r.ts < t0}
    return [r for r in records if r.client_id and r.ts is not None and r.ts >= t0 and r.client_id not in seen_before]


def upstream_for_stream(records: list[StreamRecord], client_id: str) -> list[str]:
    """Distinct mongot upstream hosts that served a given client-id (re-route check)."""
    seen: list[str] = []
    for r in records:
        if r.client_id == client_id and r.upstream_host and r.upstream_host not in seen:
            seen.append(r.upstream_host)
    return seen


def upstream_hosts(records: list[StreamRecord]) -> set[str]:
    """All distinct mongot endpoints envoy routed to across the records."""
    return {r.upstream_host for r in records if r.upstream_host}


# Admin /stats ----------------------------------------------------------------


@dataclass
class EnvoyAdminStats:
    """Parsed envoy admin ``/stats`` counters (``name: value`` text form)."""

    counters: dict[str, int]

    @classmethod
    def fetch(
        cls,
        namespace: str,
        pod: str,
        *,
        api_client: Optional[client.ApiClient] = None,
    ) -> "EnvoyAdminStats":
        """GET the admin /stats through the apiserver pod-proxy (curl-free).

        /stats is on the admin allow-list (envoy_config_builder.go); the admin port has no
        Service, so we proxy straight to the pod port via the apiserver.
        """
        core_v1 = client.CoreV1Api(api_client=api_client)
        raw = core_v1.connect_get_namespaced_pod_proxy_with_path(
            name=f"{pod}:{ENVOY_ADMIN_PORT}", namespace=namespace, path="stats"
        )
        return cls(counters=cls._parse(raw))

    @staticmethod
    def _parse(raw: str) -> dict[str, int]:
        counters: dict[str, int] = {}
        for line in raw.splitlines():
            if ": " not in line:
                continue
            key, _, value = line.partition(": ")
            try:
                counters[key.strip()] = int(value.strip())
            except ValueError:
                continue  # histograms / gauges with non-int values
        return counters

    def get(self, key: str) -> Optional[int]:
        return self.counters.get(key)

    def sum_matching(self, substring: str) -> int:
        """Sum every counter whose name contains ``substring`` (stat_prefix/cluster vary)."""
        return sum(v for k, v in self.counters.items() if substring in k)

    @property
    def total_listeners_draining(self) -> int:
        return self.get("listener_manager.total_listeners_draining") or 0

    @property
    def downstream_cx_drain_close(self) -> int:
        """Downstream connections closed because the listener was draining (GOAWAY path)."""
        return self.sum_matching("downstream_cx_drain_close")

    @property
    def server_total_connections(self) -> int:
        return self.get("server.total_connections") or 0

    def upstream_cx_total(self) -> int:
        """Total upstream (mongot) connections across all clusters."""
        return self.sum_matching("upstream_cx_total")
