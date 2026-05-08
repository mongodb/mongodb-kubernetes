"""E2E test for the background availability tester (KUBE-26).

Layered on the connectivity tool from KUBE-17. Drives
``SearchAvailabilityBackgroundTester`` against a single-cluster managed-LB
MongoDBSearch deployment and proves two things:

1. **Steady-state probe works.** A no-fault observation window over
   the running cluster produces a verdict where every page succeeded
   (``failed == 0``) and at least N iterations were recorded. This
   is the smoke test for the harness itself — without it, every
   downstream KUBE-27 failure-mode scenario would risk silently
   passing on a tester that never iterates at all.

2. **Ad-hoc outage is detected.** A deliberately broken cluster — we
   delete the mongot pod mid-window — produces a verdict where
   ``cursor_lost > 0`` (the cursor's server-side state is gone) or
   ``transient_network > 0`` (envoy returned 'no healthy upstream'
   while the new pod was scheduling). This is the deliverable signal
   per the KUBE-26 acceptance criteria: "Demonstrates on a
   deliberately broken cluster that the tester detects availability
   loss."

KUBE-27 will enumerate the 5 specific failure modes (mongot restart,
envoy restart, query before search index built, mongod missing search
params, new shard added mid-flight) on this same harness — that's why
the proof here is intentionally small and ad-hoc.

The deployment scaffolding is inherited from the three test-class mixins in
``tests.common.search.bootstrap_test_mixins``.
"""

from __future__ import annotations

import time

from kubernetes import client
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
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

# Steady-state window length. Long enough for the tester to record a
# meaningful number of iterations (>= 10 with the default 1s wait_sec)
# without dragging the suite out.
STEADY_STATE_WINDOW_SECONDS = 12.0

# Outage scenario window. Roughly 6s of healthy probing, then the fault
# (delete mongot pod), then ~25s for the new pod to schedule and the
# tester to record the failure. Tuned by hand to be short but reliable.
OUTAGE_HEALTHY_WINDOW_SECONDS = 6.0
OUTAGE_FAULT_WINDOW_SECONDS = 25.0


@mark.e2e_search_availability_background_tester
class TestSearchAvailabilityBackgroundTester(
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
        cfg.mdb_resource_name = "mdb-rs-bg-tester"
        return cfg

    # ------------------------------------------------------------------
    # Background availability tester scenarios — KUBE-26 deliverable.
    # The 15 bootstrap test methods above are inherited from the mixins.
    # ------------------------------------------------------------------

    def test_steady_state_window_reports_alive(self, mdb: MongoDB):
        """Run the tester for a short fault-free window; verdict must be clean.

        This is the smoke test for the harness — it must drive enough
        paging traffic that the verdict shows >= min_iterations recorded
        and zero failures. Without this baseline, the outage scenario
        below could pass for the wrong reasons (e.g. the harness never
        iterates and reports "0 succeeded, 0 failed" which trivially
        satisfies most failure assertions).
        """
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)
        tester = SearchAvailabilityBackgroundTester(
            tool,
            mode="paging",
            wait_sec=0.5,
            paging_batch_size=10,
        )

        tester.start()
        try:
            time.sleep(STEADY_STATE_WINDOW_SECONDS)
        finally:
            tester.stop()
            tester.join(timeout=10)
            assert not tester.is_alive(), "background tester thread did not exit cleanly"

        verdict = tester.assert_steady_state(
            min_iterations=8,
            max_failed=0,
        )
        logger.info(f"steady-state verdict: {verdict.as_dict()}")

    def test_outage_window_detects_availability_loss(self, mdb: MongoDB, mdbs: MongoDBSearch, namespace: str):
        """Drive a deliberate fault and prove the tester catches it.

        Run a short healthy window so the verdict has at least one
        upstream-confirmed page first, then take ALL mongot pods offline
        (``deliberately broken cluster`` per KUBE-26 acceptance). Continue
        probing through the outage window so the tester records the
        resulting failures, then let the StatefulSet's controller bring
        mongot back.

        The managed-LB fixture has ``spec.replicas: 2``; deleting only
        pod-0 leaves pod-1 healthy through the entire fault window and
        the tester sees zero failures — exactly the false-green this
        test exists to prevent. We delete every pod that matches the
        StatefulSet's pod-template label (``app=<sts-name>-svc``) in one
        pass. KUBE-27's failure-mode scenarios use the same selector for
        the same reason.

        Asserts the verdict surfaces either:
        - ``cursor_lost > 0`` — a long-living cursor's server-side state
          is gone, surfaced as ``OperationFailure(code=1)`` "Remote error
          from mongot :: RST_STREAM" by mongod.
        - ``transient_network > 0`` — envoy returns "no healthy upstream"
          because all mongot pods are gone.

        Either is acceptable evidence that the tester actually detects
        availability loss.
        """
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)
        # Use oneshot mode rather than paging here. A long-living cursor's
        # getMore can be served from mongod's server-side cache during a
        # mongot outage, masking the fault — that's the cache caveat the
        # connectivity tool was specifically designed to expose. Oneshot
        # queries open a fresh aggregation each iteration, which must
        # actually evaluate against mongot every time, so when mongot is
        # gone every fresh query fails (envoy returns "no healthy upstream"
        # or mongot connection refused).
        tester = SearchAvailabilityBackgroundTester(
            tool,
            mode="oneshot",
            wait_sec=0.5,
        )

        statefulset_name = search_resource_names.mongot_statefulset_name(mdbs.name)

        tester.start()
        try:
            # Phase 1 — let the tester run cleanly so the verdict has a
            # known-good baseline. We assert this baseline at the end.
            time.sleep(OUTAGE_HEALTHY_WINDOW_SECONDS)
            pre_results = tester.get_results()
            logger.info(f"pre-fault iterations recorded: {len(pre_results)}")
            assert any(p.success for p in pre_results), (
                f"pre-fault window has no successful iteration; harness isn't iterating. "
                f"results={[str(r) for r in pre_results]}"
            )

            # Phase 2 — induce a brief outage by deleting EVERY mongot pod.
            # The StatefulSet's controller will recreate them within ~10-30s,
            # but during the recreation window the cursor's gRPC stream to
            # mongot is dead and envoy has no healthy upstream — so any
            # post-fault getMore must fail (cursor_lost because the new
            # pod has no record of the cursor's session, or
            # transient_network because envoy briefly has no healthy
            # upstream).
            #
            # We use pod-delete rather than CR-driven scale-to-0 here
            # because the python kubernetes-client's openapi serializer
            # silently drops zero-valued int fields when patching custom
            # objects: ``mdbs["spec"]["replicas"] = 0; mdbs.update()`` does
            # NOT actually propagate to the API. Pod deletion via
            # core_v1.delete_namespaced_pod sidesteps that.
            #
            # Crucially, we have to delete EVERY replica. The managed-LB
            # fixture has ``spec.replicas: 2``; deleting only pod-0 leaves
            # pod-1 healthy through the entire fault window and the
            # tester sees zero failures — the exact false-green this test
            # exists to prevent. The StatefulSet's pod template carries
            # ``app=<sts-name>-svc`` (the headless service selector), so
            # using that label catches every replica in one shot. KUBE-27
            # uses the same selector for the same reason.
            core_v1 = client.CoreV1Api()
            pod_label = f"app={statefulset_name}-svc"
            pods = core_v1.list_namespaced_pod(
                namespace=mdb.namespace,
                label_selector=pod_label,
            ).items
            assert pods, f"no mongot pods matched selector '{pod_label}' in namespace {mdb.namespace}"
            for p in pods:
                logger.info(f"deleting mongot pod {p.metadata.name} (uid={p.metadata.uid})")
                core_v1.delete_namespaced_pod(name=p.metadata.name, namespace=mdb.namespace)

            # Phase 3 — keep probing through the outage. Even if the
            # StatefulSet recovers quickly, the cursor's pre-existing
            # gRPC stream is dead and the new mongot pod has no record
            # of the cursor's session.
            time.sleep(OUTAGE_FAULT_WINDOW_SECONDS)
        finally:
            tester.stop()
            tester.join(timeout=10)
            assert not tester.is_alive(), "background tester thread did not exit cleanly"

            # Cleanup — wait for the StatefulSet's controller to recreate
            # mongot pods so sibling tests see a healthy cluster.
            try:
                mdbs.assert_reaches_phase(Phase.Running, timeout=180)
            except Exception as e:
                logger.warning(f"cleanup mdbs.assert_reaches_phase(Running) timed out: {e}")

        verdict = tester.assert_outage_detected(
            accept_classes=("cursor_lost", "transient_network"),
        )
        logger.info(f"outage-window verdict: {verdict.as_dict()}")
        assert verdict.total > 0, (
            f"outage-window verdict has no iterations at all — the harness never ran. " f"verdict={verdict.as_dict()}"
        )
