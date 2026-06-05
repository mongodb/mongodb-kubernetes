"""Shared helpers for the search upgrade-path availability suites.

Used by tests/search/search_availability_upgrade_dataplane.py and the operator/chart
upgrade suites in tests/upgrades/. Kept to pure functions (roll counting + a structured
metric log line) so each suite keeps its own deploy chain and background-tester window.
"""

from __future__ import annotations

import logging

from kubetester import list_matching_pods

logger = logging.getLogger(__name__)


def pod_uids(namespace: str, label_selector: str) -> dict[str, str]:
    return {p.metadata.name: p.metadata.uid for p in list_matching_pods(namespace, label_selector=label_selector)}


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
