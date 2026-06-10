"""Shared helpers for the search upgrade-path availability suites: roll counting, a metric log line,
and the ride-through assertion. Each suite keeps its own deploy chain and background-tester window."""

from __future__ import annotations

import logging
import time
from typing import Callable, Optional

from kubernetes import client
from kubetester import list_matching_pods
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester, assert_no_outage
from tests.common.search.connectivity import SearchConnectivityTool

logger = logging.getLogger(__name__)

BASELINE_OPS = 30
POST_EVENT_OPS = 15


def pod_uids(namespace: str, label_selector: str) -> dict[str, str]:
    return {p.metadata.name: p.metadata.uid for p in list_matching_pods(namespace, label_selector=label_selector)}


def container_pod_uids(namespace: str, container_name: str) -> dict[str, str]:
    """Pod name->uid for pods running a container with this name. Container-name based so it survives
    the StatefulSet/Deployment rename an operator upgrade can cause (a label snapshot would miss them)."""
    pods = client.CoreV1Api().list_namespaced_pod(namespace).items
    return {
        p.metadata.name: p.metadata.uid
        for p in pods
        if p.spec.containers and any(c.name == container_name for c in p.spec.containers)
    }


def gone_or_changed(before_uids: dict[str, str], after_uids: dict[str, str]) -> int:
    """Pods replaced since the snapshot: a uid changed under the same name, or a name that vanished."""
    changed = sum(1 for name, uid in after_uids.items() if name in before_uids and before_uids[name] != uid)
    gone = sum(1 for name in before_uids if name not in after_uids)
    return max(changed, gone)


def roll_count(namespace: str, label_selector: str, before_uids: dict[str, str]) -> int:
    """Pods replaced since the pre-upgrade snapshot (StatefulSet keeps the name with a new uid;
    Deployment gets fresh names) — count both as a roll."""
    after = pod_uids(namespace, label_selector)
    changed = sum(1 for name, uid in after.items() if before_uids.get(name) != uid)
    gone = sum(1 for name in before_uids if name not in after)
    return max(changed, gone)


def emit_metric(path: str, *, rolls_mongot: int, rolls_envoy: int, recovery_s: float, disruption_s: float) -> None:
    """One greppable SEARCH_UPGRADE_METRIC line per path; the roll/disruption stories cite it from the log."""
    logger.info(
        f"SEARCH_UPGRADE_METRIC path={path} rolls_mongot={rolls_mongot} rolls_envoy={rolls_envoy} "
        f"recovery_s={recovery_s:.1f} disruption_s={disruption_s:.1f}"
    )


def assert_rolled_through(oneshot_verdict, context: str, *, disruption_s: float, bound_s: float) -> None:
    """New-query availability across a roll: outage bounded (oneshot failure streak <= bound_s) and no unexpected failure class (transient blips OK)."""
    logger.info(f"{context} oneshot verdict: {oneshot_verdict.as_dict()} disruption_s={disruption_s:.1f}s")
    assert (
        disruption_s <= bound_s
    ), f"{context}: sustained new-query outage {disruption_s:.1f}s exceeded {bound_s:.1f}s bound; {oneshot_verdict.as_dict()}"
    assert (
        oneshot_verdict.other_failed == 0 and oneshot_verdict.cursor_lost == 0
    ), f"{context}: unexpected new-query failure class; {oneshot_verdict.as_dict()}"


def run_upgrade_availability(
    namespace: str,
    *,
    tool_factory: Callable[[], SearchConnectivityTool],
    apply_upgrade: Callable[[], None],
    path: str,
    disruption_bound_s: Optional[float] = None,
    oneshot_accept: tuple[str, ...] = ("transient_network",),
) -> None:
    """Drive continuous paging+oneshot load across an upgrade; emit a roll + recovery/disruption metric.
    ``apply_upgrade`` performs the change and blocks until both planes reconverge. Asserts new queries
    hit no failure class outside ``oneshot_accept`` (empty tuple demands a zero-failure window) and any
    outage stayed within ``disruption_bound_s``."""

    tool_factory().wait_for_sentinel_indexed(timeout=300)
    mongot_before = container_pod_uids(namespace, "mongot")
    envoy_before = container_pod_uids(namespace, "envoy")
    oneshot = SearchAvailabilityBackgroundTester(tool_factory(), mode="oneshot", interval_seconds=0.2)
    paging = SearchAvailabilityBackgroundTester(
        tool_factory(), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
    )
    with oneshot, paging:
        oneshot.wait_for_operations(BASELINE_OPS)
        paging.wait_for_operations(BASELINE_OPS)
        t0 = time.monotonic()
        apply_upgrade()
        recovery_s = time.monotonic() - t0
        oneshot.wait_for_operations(POST_EVENT_OPS)
        paging.wait_for_operations(POST_EVENT_OPS)
    disruption_s = oneshot.max_failure_window_s
    rolls_mongot = gone_or_changed(mongot_before, container_pod_uids(namespace, "mongot"))
    rolls_envoy = gone_or_changed(envoy_before, container_pod_uids(namespace, "envoy"))
    verdict = oneshot.verdict
    logger.info(f"{path} oneshot verdict: {verdict.as_dict()}")
    logger.info(f"{path} paging verdict (observational): {paging.verdict.as_dict()}")
    emit_metric(
        path, rolls_mongot=rolls_mongot, rolls_envoy=rolls_envoy, recovery_s=recovery_s, disruption_s=disruption_s
    )
    if disruption_bound_s is not None:
        assert (
            disruption_s <= disruption_bound_s
        ), f"{path}: new-query outage {disruption_s:.1f}s exceeded bound {disruption_bound_s:.1f}s; {verdict.as_dict()}"
    if not oneshot_accept:
        assert_no_outage(verdict)
    else:
        # accepted classes may blip during the roll; recovery gated by the trailing steady window
        rejected = {
            klass: count
            for klass, count in (
                ("cursor_lost", verdict.cursor_lost),
                ("transient_network", verdict.transient_network),
                ("other", verdict.other_failed),
            )
            if klass not in oneshot_accept and count > 0
        }
        assert (
            not rejected
        ), f"{path}: new-query failures outside accepted classes {oneshot_accept}: {rejected}; {verdict.as_dict()}"
    with SearchAvailabilityBackgroundTester(tool_factory(), mode="oneshot", interval_seconds=0.1) as bg:
        bg.wait_for_operations(BASELINE_OPS)
    assert_no_outage(bg.verdict)
