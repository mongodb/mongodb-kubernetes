"""Shared helpers for the search upgrade-path availability suites.

Used by tests/search/search_availability_upgrade_dataplane.py and the operator/chart
upgrade suites in tests/upgrades/. Kept to pure functions (roll counting + a structured
metric log line) so each suite keeps its own deploy chain and background-tester window.
"""

from __future__ import annotations

import logging
import time
from typing import Callable, Optional

from kubernetes import client
from kubetester import list_matching_pods
from tests.common.search.background_availability_tester import (
    SearchAvailabilityBackgroundTester,
    assert_no_outage,
)
from tests.common.search.connectivity import SearchConnectivityTool

logger = logging.getLogger(__name__)

BASELINE_OPS = 30
POST_EVENT_OPS = 15


def pod_uids(namespace: str, label_selector: str) -> dict[str, str]:
    return {p.metadata.name: p.metadata.uid for p in list_matching_pods(namespace, label_selector=label_selector)}


def container_pod_uids(namespace: str, container_name: str) -> dict[str, str]:
    """Pod name->uid for every pod running a container with this name. Naming-agnostic: survives
    the StatefulSet/Deployment rename when an operator upgrade changes the resource naming scheme,
    where a label-selector snapshot taken pre-upgrade would miss the renamed pods."""
    pods = client.CoreV1Api().list_namespaced_pod(namespace).items
    return {
        p.metadata.name: p.metadata.uid
        for p in pods
        if p.spec.containers and any(c.name == container_name for c in p.spec.containers)
    }


def gone_or_changed(before_uids: dict[str, str], after_uids: dict[str, str]) -> int:
    """Pods replaced since the snapshot: a uid that changed under the same name, or a name that
    disappeared (replaced under a new name by a rename)."""
    changed = sum(1 for name, uid in after_uids.items() if name in before_uids and before_uids[name] != uid)
    gone = sum(1 for name in before_uids if name not in after_uids)
    return max(changed, gone)


def roll_count(namespace: str, label_selector: str, before_uids: dict[str, str]) -> int:
    """Pods replaced since the pre-upgrade snapshot. StatefulSet pods keep their name with a new
    uid; Deployment pods get fresh names. Count both as a roll."""
    after = pod_uids(namespace, label_selector)
    changed = sum(1 for name, uid in after.items() if before_uids.get(name) != uid)
    gone = sum(1 for name in before_uids if name not in after)
    return max(changed, gone)


def emit_metric(path: str, *, rolls_mongot: int, rolls_envoy: int, recovery_s: float, disruption_s: float) -> None:
    """One greppable line per upgrade path. KUBE-24 (gratuitous rolls) and KUBE-42 (disruption
    bound) cite these numbers from the task log."""
    logger.info(
        f"KUBE40_METRIC path={path} rolls_mongot={rolls_mongot} rolls_envoy={rolls_envoy} "
        f"recovery_s={recovery_s:.1f} disruption_s={disruption_s:.1f}"
    )


def run_upgrade_availability(
    namespace: str,
    *,
    tool_factory: Callable[[], SearchConnectivityTool],
    apply_upgrade: Callable[[], None],
    path: str,
    disruption_bound_s: Optional[float] = None,
) -> None:
    """Drive a continuous paging+oneshot load across an operator/chart upgrade and emit a per-run
    roll count + recovery/disruption metric.

    ``tool_factory`` returns a fresh connectivity tool (own client) per call — one per concurrent
    background tester. ``apply_upgrade`` performs the upgrade and blocks until both planes have
    reconverged (operator Running, MongoDBSearch Running, pods Ready). The operator-upgrade deploys
    run a single mongot, so its roll is a brief outage rather than a ride-through; this asserts
    outage->recover (steady state restored after the upgrade), not zero failures.
    ``disruption_bound_s``, when set, also hard-asserts the measured disruption fits a bound."""

    tool = tool_factory

    tool().wait_for_sentinel_indexed(timeout=300)
    mongot_before = container_pod_uids(namespace, "mongot")
    envoy_before = container_pod_uids(namespace, "envoy")
    oneshot = SearchAvailabilityBackgroundTester(tool(), mode="oneshot", interval_seconds=0.2)
    paging = SearchAvailabilityBackgroundTester(
        tool(), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
    )
    with oneshot, paging:
        oneshot.wait_for_operations(BASELINE_OPS)
        paging.wait_for_operations(BASELINE_OPS)
        t0 = time.monotonic()
        apply_upgrade()
        recovery_s = time.monotonic() - t0
        oneshot.wait_for_operations(POST_EVENT_OPS)
        paging.wait_for_operations(POST_EVENT_OPS)
    disruption_s = paging.max_consecutive_failure * paging.interval_seconds
    rolls_mongot = gone_or_changed(mongot_before, container_pod_uids(namespace, "mongot"))
    rolls_envoy = gone_or_changed(envoy_before, container_pod_uids(namespace, "envoy"))
    logger.info(f"{path} oneshot verdict: {oneshot.verdict.as_dict()}")
    logger.info(f"{path} paging verdict: {paging.verdict.as_dict()}")
    emit_metric(
        path, rolls_mongot=rolls_mongot, rolls_envoy=rolls_envoy, recovery_s=recovery_s, disruption_s=disruption_s
    )
    if disruption_bound_s is not None:
        assert (
            disruption_s <= disruption_bound_s
        ), f"{path}: disruption {disruption_s:.1f}s exceeded bound {disruption_bound_s:.1f}s"
    with SearchAvailabilityBackgroundTester(tool(), mode="oneshot", interval_seconds=0.1) as bg:
        bg.wait_for_operations(BASELINE_OPS)
    assert_no_outage(bg.verdict)
