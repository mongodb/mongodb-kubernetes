"""E2E failure-mode scenarios for the background availability tester.

Each scenario starts the background tester, drives a fault, and asserts
the verdict surfaces a real failure (cursor_lost or transient_network).
If the tester silently passes through a fault, every downstream
availability test built on it becomes a no-op.
"""

from __future__ import annotations

import time

from kubernetes import client
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from pytest import mark
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester
from tests.common.search.bootstrap_test_mixins import (
    MongoDBRsDeploymentConfig,
    MongoDBRsDeploymentTests,
    SearchDeploymentTests,
    SearchE2EFixtures,
    SearchSampleDataAndIndexTests,
)
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.rs_search_helper import get_rs_search_tester

logger = test_logger.get_test_logger(__name__)

# Fault windows. Each scenario opens a short healthy probe first so the
# verdict has known-good baseline iterations, then introduces the fault
# and continues probing for long enough to capture the recovery window.
HEALTHY_BASELINE_SECONDS = 6.0
FAULT_OBSERVATION_SECONDS = 25.0


def _new_tester(
    mdb: MongoDB,
    user_name: str,
    user_password: str,
) -> SearchAvailabilityBackgroundTester:
    """Oneshot mode forces a real upstream evaluation each tick — a paging
    getMore could be served from mongod's cache during a fault window."""
    search_tester = get_rs_search_tester(mdb, user_name, user_password, use_ssl=True)
    tool = SearchConnectivityTool(search_tester)
    return SearchAvailabilityBackgroundTester(
        tool,
        mode="oneshot",
        wait_sec=0.5,
    )


def _wait_for_pod_recreation(
    namespace: str,
    label_selector: str,
    excluded_uids: set[str],
    timeout: int = 180,
) -> None:
    """Block until at least one pod matching label_selector is Ready
    AND its uid is NOT in excluded_uids.

    Used by every fault scenario's cleanup so the next scenario
    starts on a healthy cluster.
    """
    core_v1 = client.CoreV1Api()

    def _ready_with_new_uid() -> tuple[bool, str]:
        pods = core_v1.list_namespaced_pod(namespace=namespace, label_selector=label_selector).items
        if not pods:
            return False, "no pods match selector"
        fresh = [
            p
            for p in pods
            if p.metadata.uid not in excluded_uids
            and any(c.type == "Ready" and c.status == "True" for c in (p.status.conditions or []))
        ]
        return len(fresh) > 0, f"matching={len(pods)} fresh-and-ready={len(fresh)}"

    run_periodically(
        _ready_with_new_uid,
        timeout=timeout,
        sleep_time=5,
        msg=f"pod recreation for selector '{label_selector}'",
    )


def _delete_pods_in_label(namespace: str, label_selector: str) -> set[str]:
    """Delete every pod matching label_selector, return their old uids."""
    core_v1 = client.CoreV1Api()
    pods = core_v1.list_namespaced_pod(namespace=namespace, label_selector=label_selector).items
    if not pods:
        # Some resources don't have the canonical pod-name label; the
        # caller is responsible for providing a working selector.
        return set()
    uids = {p.metadata.uid for p in pods}
    for p in pods:
        logger.info(f"deleting pod {p.metadata.name} (uid={p.metadata.uid})")
        core_v1.delete_namespaced_pod(name=p.metadata.name, namespace=namespace)
    return uids


@mark.e2e_search_failure_modes
class TestSearchFailureModes(
    # Bases listed in REVERSE execution order — pytest emits inherited
    # tests in reversed(MRO) so the FIRST base runs LAST. See module
    # docstring of bootstrap_test_mixins for the full rule.
    SearchSampleDataAndIndexTests,  # runs LAST  (Layer 3 — sample data + index + smoke)
    SearchDeploymentTests,  # runs second (Layer 2 — MongoDBSearch + envoy)
    MongoDBRsDeploymentTests,  # runs FIRST  (Layer 1 — operator + MongoDB)
    SearchE2EFixtures,  # fixtures + default config builders
):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        cfg = super().build_mongodb_rs_config()
        # Unique resource name so this e2e can run on a warm cluster
        # without collisions.
        cfg.mdb_resource_name = "mdb-rs-fail-modes"
        return cfg

    # ------------------------------------------------------------------
    # Failure-mode scenarios — KUBE-27 deliverable. The 15 bootstrap
    # test methods above are inherited from the chained mixins.
    # ------------------------------------------------------------------

    def test_failure_mongot_pod_restart_surfaces_outage(
        self,
        mdb: MongoDB,
        mdbs: MongoDBSearch,
        namespace: str,
    ):
        """Scenario 1 — mongot pod restart.

        Delete EVERY mongot pod. The search-rs-managed-lb fixture has
        ``spec.replicas: 2``; deleting only one pod would leave the
        other replica serving and the tester would see no fault. The
        StatefulSet controller recreates each pod within ~10-30s; during
        the recreation window envoy has no healthy upstream, so the
        tester's oneshot probes must surface ``transient_network``
        failures (or ``cursor_lost`` if the timing catches a cursor
        mid-getMore).
        """
        statefulset_name = search_resource_names.mongot_statefulset_name(mdbs.name)
        # StatefulSet's pod template has ``app=<sts-name>-svc`` (matches
        # the headless service selector); using this label catches every
        # replica rather than just pod-0.
        pod_label = f"app={statefulset_name}-svc"

        cfg = self.build_mongodb_rs_config()
        tester = _new_tester(mdb, cfg.user_name, cfg.user_password)
        tester.start()
        try:
            time.sleep(HEALTHY_BASELINE_SECONDS)
            excluded = _delete_pods_in_label(namespace, pod_label)
            assert excluded, f"no mongot pods matched selector '{pod_label}'"
            time.sleep(FAULT_OBSERVATION_SECONDS)
        finally:
            tester.stop()
            tester.join(timeout=10)
            assert not tester.is_alive(), "background tester thread did not exit cleanly"
            try:
                _wait_for_pod_recreation(namespace, pod_label, excluded)
            except Exception as e:
                logger.warning(f"mongot pod recreation wait timed out in cleanup: {e}")

        verdict = tester.assert_outage_detected(accept_classes=("cursor_lost", "transient_network"))
        logger.info(f"mongot-restart verdict: {verdict.as_dict()}")
        assert verdict.total > 0, (
            f"verdict has no iterations at all — the harness never ran. " f"verdict={verdict.as_dict()}"
        )
