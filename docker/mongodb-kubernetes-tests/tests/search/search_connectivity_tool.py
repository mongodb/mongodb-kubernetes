"""E2E test for the search connectivity tool (KUBE-17).

Drives ``SearchConnectivityTool`` against a single-cluster managed-LB
MongoDBSearch deployment and proves the wire-op-counting logic actually
works — by taking mongot down mid-paging via the operator and asserting the
tool surfaces the resulting connectivity errors rather than reporting a
verdict with zero failures.

The deployment scaffolding is inherited from the three test-class mixins in
``tests.common.search.bootstrap_test_mixins``.
"""

from __future__ import annotations

from kubernetes import client
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import mark
from tests import test_logger
from tests.common.search import search_resource_names
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


@mark.e2e_search_connectivity_tool
class TestSearchConnectivityTool(
    # Bases listed in REVERSE execution order — pytest emits inherited
    # tests in reversed(MRO) so the FIRST base runs LAST. See module
    # docstring of bootstrap_test_mixins for the full rule.
    SearchSampleDataAndIndexTests,  # runs LAST  (Layer 3 — sample data + index + smoke)
    SearchDeploymentTests,          # runs second (Layer 2 — MongoDBSearch + envoy)
    MongoDBRsDeploymentTests,       # runs FIRST  (Layer 1 — operator + MongoDB)
    SearchE2EFixtures,              # fixtures + default config builders
):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        cfg = super().build_mongodb_rs_config()
        # Unique resource name so this e2e can run on a warm cluster
        # without collisions.
        cfg.mdb_resource_name = "mdb-rs-conn-tool"
        return cfg

    # ------------------------------------------------------------------
    # Connectivity tool tests — the actual KUBE-17 deliverable. The 15
    # bootstrap test methods above are inherited from the mixins.
    # ------------------------------------------------------------------

    def test_oneshot_search_succeeds_and_reports_upstream(self, mdb: MongoDB):
        """One-shot search with cache-busted query — must reach mongod."""
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        result = tool.oneshot_search()
        logger.info(f"oneshot_search result: {result}")
        assert result.success, f"one-shot search failed: {result.error_class} {result.error_message}"
        assert result.returned_count > 0, "expected some results from cache-busted compound query"
        # A one-shot aggregate ALWAYS issues at least one wire op to mongod
        # — the pymongo CommandListener captures every CommandStartedEvent
        # for aggregate / getMore / killCursors. Zero wire ops would mean
        # the tool's listener didn't attach to the underlying MongoClient.
        assert result.mongod_wire_ops > 0, (
            f"one-shot aggregate reported mongod_wire_ops={result.mongod_wire_ops}; "
            f"expected >= 1 — the CommandListener is not firing. wire_ops={result.wire_ops}"
        )

        verdict = tool.verdict([result])
        assert verdict.hit_mongod_observed, f"verdict.hit_mongod_observed should be True; got {verdict.as_dict()}"

    def test_paging_search_first_page_is_upstream(self, mdb: MongoDB):
        """First paging page corresponds to the cursor's firstBatch — wire op fires."""
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        pages = tool.paging_search(pages=3, interval_seconds=0.1, batch_size=20)
        logger.info("paging_search results: %s", "; ".join(str(p) for p in pages))
        assert pages, "paging_search returned no pages"
        assert pages[0].success, f"first page failed: {pages[0].error_class} {pages[0].error_message}"
        # Page 0 of a fresh paging cursor opens the cursor by issuing an
        # ``aggregate`` command — that's at least one wire op observed by
        # the CommandListener. The CommandListener's started count is the
        # ground-truth replacement for the old buffer-probe heuristic.
        assert pages[0].mongod_wire_ops > 0, (
            f"first page should always issue at least one wire op; got {pages[0]}"
        )
        assert pages[0].returned_count > 0, "first page returned 0 docs"

    def test_paging_search_first_page_is_upstream2(self, mdb: MongoDB):
        """First paging page corresponds to the cursor's firstBatch — wire op fires."""
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        pages = tool.paging_search(pages=10, interval_seconds=0.1, batch_size=20)
        logger.info("paging_search results: %s", "; ".join(str(p) for p in pages))
        assert pages, "paging_search returned no pages"
        assert pages[0].success, f"first page failed: {pages[0].error_class} {pages[0].error_message}"
        assert pages[0].mongod_wire_ops > 0, (
            f"first page should always issue at least one wire op; got {pages[0]}"
        )
        assert pages[0].returned_count > 0, "first page returned 0 docs"


    def test_paging_through_mongot_outage_surfaces_connectivity_error(self, mdb: MongoDB, mdbs: MongoDBSearch):
        """Cache-distinguishing assertion — the deliverable signal of KUBE-17.

        Open a paging cursor against a healthy mongot, then scale the
        MongoDBSearch CR to 0 replicas via the operator and continue paging.
        The connectivity tool must surface a real connectivity-class error
        from at least one post-outage page — a success that was served from
        pymongo's local buffer (``mongod_wire_ops == 0``) tells us nothing
        about upstream availability.

        NOTE: this test only exercises the "no healthy upstream" path produced
        by taking all mongots away via the operator. The "lost long-living
        cursor" path (mongot/envoy/mongod restarts mid-cursor) is intentionally
        out of scope here and will land in a follow-up KUBE ticket.
        """
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        # Open a cursor while mongot is healthy and confirm the harness
        # produces at least one page with a real wire op. Two pages with a
        # small batch is enough to cross at least one getMore boundary.
        pre_pages = tool.paging_search(pages=2, interval_seconds=0.1, batch_size=10)
        logger.info("pre-outage pages: %s", "; ".join(str(p) for p in pre_pages))
        assert any(p.success and p.mongod_wire_ops > 0 for p in pre_pages), (
            "expected at least one wire-op page before scaling mongot down; "
            "the CommandListener didn't fire — the tool isn't actually probing mongod"
        )

        # Drive the outage via the operator: set spec.replicas=0 on the
        # MongoDBSearch CR and let the reconciler drain the underlying mongot
        # StatefulSet. The CRD allows minimum: 0 on spec.replicas (and on
        # spec.clusters[].replicas) precisely so callers like this test can
        # take mongot offline cleanly without bypassing the operator.
        statefulset_name = search_resource_names.mongot_statefulset_name(mdbs.name)
        apps_v1 = client.AppsV1Api()
        logger.info(f"setting MongoDBSearch {mdbs.name} spec.replicas -> 0 via operator")
        mdbs["spec"]["replicas"] = 0
        mdbs.update()

        def mongot_pods_gone() -> tuple[bool, str]:
            # The MongoDBSearch controller's reconciler does NOT scale the
            # mongot StatefulSet to 0 when ``spec.replicas: 0`` — it deletes
            # the StatefulSet entirely. So treating the resulting 404 as
            # "pods gone" is correct here (and the only way this loop will
            # ever return True). ``run_periodically`` swallows exceptions
            # and retries, which would otherwise hide this behavior behind
            # a 3-minute timeout.
            try:
                sts = apps_v1.read_namespaced_stateful_set(statefulset_name, mdb.namespace)
            except client.exceptions.ApiException as exc:
                if exc.status == 404:
                    return True, "statefulset deleted by reconciler"
                raise
            ready = sts.status.ready_replicas or 0
            return ready == 0, f"ready_replicas={ready}"

        run_periodically(
            mongot_pods_gone,
            timeout=180,
            sleep_time=5,
            msg=f"mongot StatefulSet {statefulset_name} to drain (delete or scale to 0)",
        )

        # Now run a fresh paging cursor against the broken cluster. We expect
        # at least one connectivity error — pymongo surfaces "no healthy
        # upstream" as ``OperationFailure`` because envoy returns a non-200
        # to the mongot RPC. Cache-only successes are noise here; the load-
        # bearing assertion is "we observed a real failure".
        post_pages = tool.paging_search(pages=8, interval_seconds=0.5, batch_size=10)
        logger.info("post-outage pages: %s", "; ".join(str(p) for p in post_pages))

        post_verdict = tool.verdict(post_pages)
        logger.info(f"post-outage verdict: {post_verdict.as_dict()}")

        # Deliverable assertion 1: at least one connectivity error must surface.
        # A success that was served entirely from pymongo's local buffer
        # (``mongod_wire_ops == 0``) on its own is not informative —
        # we need a real failure to know the tool is propagating upstream-loss
        # instead of silently swallowing it. See the reviewer's note on PR #1080.
        assert post_verdict.failed > 0, (
            f"post-outage verdict has no failures — the connectivity tool isn't surfacing "
            f"the upstream loss. Verdict: {post_verdict.as_dict()}"
        )
        # Failures are expected to be pymongo ``OperationFailure`` (envoy
        # returns "no healthy upstream") or ``ServerSelectionTimeoutError`` /
        # ``NetworkTimeout`` — anything in the connectivity family. Reject
        # plain "Unknown" since that means error classification broke.
        expected_error_classes = {
            "OperationFailure",
            "ServerSelectionTimeoutError",
            "NetworkTimeout",
            "AutoReconnect",
            "ConnectionFailure",
        }
        observed_error_classes = set(post_verdict.error_breakdown)
        assert observed_error_classes & expected_error_classes, (
            f"post-outage failures did not include any expected connectivity-class error; "
            f"got error_breakdown={post_verdict.error_breakdown}. "
            f"Expected one of {sorted(expected_error_classes)}."
        )

        # Cleanup: bring mongot back via the operator. Setting
        # spec.replicas=1 on the CR and waiting for Phase.Running is the
        # symmetric counterpart to the scale-down above, and exercises the
        # operator's recovery path (StatefulSet recreation + readiness)
        # rather than bypassing it with a direct StatefulSet patch.
        logger.info(f"setting MongoDBSearch {mdbs.name} spec.replicas -> 1 via operator")
        mdbs["spec"]["replicas"] = 1
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=300)

    def test_paging_through_mongot_pod_restart_surfaces_lost_cursor(self, mdb: MongoDB, mdbs: MongoDBSearch):
        """Cursor-lost assertion — pod restart kills the cursor's server-side state.

        Distinct failure mode from ``test_paging_through_mongot_outage_surfaces_connectivity_error``.
        That test takes mongot offline entirely (envoy returns 503 → ``no
        healthy upstream``); here we leave the StatefulSet at replicas=1 and
        just delete the mongot pod, so the StatefulSet immediately recreates
        a fresh pod. The new pod has no memory of the open cursor's
        server-side state, so the next ``getMore`` on that cursor surfaces a
        cursor-lost error rather than a transient blip.

        The surface error here is NOT pymongo's classic ``CursorNotFound``
        (server error code 43). Mongod surfaces the mongot-side stream
        death as ``OperationFailure(code=1, codeName=InternalError)`` whose
        message reads ``"Executor error during getMore :: caused by ::
        Remote error from mongot :: caused by :: Received RST_STREAM with
        error code 2"`` — the gRPC stream between mongod and mongot was
        reset when the mongot pod died, and the new mongot pod has no
        record of the cursor's session-side state.
        ``classify_failure`` recognises both the canonical CursorNotFound
        and the "Remote error from mongot" / "RST_STREAM" signal patterns,
        mapping both to the ``cursor_lost`` bucket.

        Transient ``no_healthy_upstream`` errors that pop up while the new
        pod is starting are absorbed by the retry-once-noted path in
        ``paging_cursor_read_pages``; the test asserts on the
        ``cursor_lost`` bucket of the post-restart verdict rather than on
        the raw error_breakdown so flakiness on the transient side doesn't
        fail the test.

        Why ``paging_cursor_open`` + ``paging_cursor_read_pages`` rather than
        a single ``paging_search`` call: this test needs to keep the SAME
        cursor across the pod restart, so we open it explicitly, do a
        fault, and continue reading on the same handle. The wrapper
        ``paging_search`` always opens + closes inside one call, which would
        not exercise the cursor-lost path.
        """
        cfg = self.build_mongodb_rs_config()
        search_tester = get_rs_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        statefulset_name = search_resource_names.mongot_statefulset_name(mdbs.name)
        core_v1 = client.CoreV1Api()
        namespace = mdb.namespace

        # Open a paging cursor while mongot is healthy and read a couple of
        # pages to confirm it's alive and we're crossing at least one
        # getMore boundary on the buffer-probe heuristic.
        cursor = tool.paging_cursor_open(batch_size=10)
        try:
            pre_pages = tool.paging_cursor_read_pages(
                cursor,
                pages=2,
                interval_seconds=0.1,
                batch_size=10,
                first_page_index=0,
            )
            logger.info("pre-restart pages: %s", "; ".join(str(p) for p in pre_pages))
            assert all(p.success for p in pre_pages), (
                f"pre-restart pages failed before we even introduced a fault: "
                f"{[(p.page_index, p.error_class, p.error_message) for p in pre_pages if not p.success]}"
            )
            assert any(p.success and p.mongod_wire_ops > 0 for p in pre_pages), (
                "expected at least one pre-restart page to issue a wire op; "
                "the CommandListener didn't fire — the cursor isn't actually contacting mongod"
            )

            # Identify the pod backing this cursor's mongot replica. With
            # spec.replicas=1 there's only one mongot pod and it owns the
            # cursor's server-side state. Delete it; the StatefulSet's
            # controller recreates a fresh pod with the same name but a
            # new uid, with no prior cursor state on the new instance.
            # We list by name prefix rather than label selector so this
            # stays robust to label-key drift across operator releases.
            mongot_pod_names = [
                p.metadata.name
                for p in core_v1.list_namespaced_pod(namespace=namespace).items
                if p.metadata.name.startswith(statefulset_name + "-")
            ]
            assert mongot_pod_names, f"no mongot pods found for StatefulSet {statefulset_name}"
            target_pod = mongot_pod_names[0]
            logger.info(f"deleting mongot pod {target_pod} to invalidate the cursor's server-side state")
            original_uid = core_v1.read_namespaced_pod(name=target_pod, namespace=namespace).metadata.uid
            core_v1.delete_namespaced_pod(name=target_pod, namespace=namespace)

            # Wait for the StatefulSet to bring the pod back. We watch by
            # UID change rather than ready_replicas swing because on a
            # fast-recreate the StatefulSet may never observe ready_replicas
            # actually drop to 0 (the controller finishes the delete and
            # recreate before the watch fires).
            def mongot_pod_replaced() -> tuple[bool, str]:
                try:
                    pod = core_v1.read_namespaced_pod(name=target_pod, namespace=namespace)
                except client.exceptions.ApiException as exc:
                    if exc.status == 404:
                        return False, f"{target_pod} still terminating"
                    raise
                if pod.metadata.uid == original_uid:
                    return False, f"{target_pod} same uid (delete still pending)"
                ready = any(c.type == "Ready" and c.status == "True" for c in (pod.status.conditions or []))
                return ready, f"{target_pod} uid={pod.metadata.uid[:8]} ready={ready}"

            run_periodically(
                mongot_pod_replaced,
                timeout=180,
                sleep_time=3,
                msg=f"mongot pod {target_pod} to be replaced by a fresh instance",
            )

            # Continue paging on the SAME cursor. The fresh mongot pod has
            # no memory of this cursor's server-side state, so the next
            # getMore should produce a cursor-lost error. We page a generous
            # number of times because the retry-once-noted path will absorb
            # any transient envoy 503 that fires while the new pod is just
            # coming up — we want to keep paging until the cursor-lost is
            # surfaced.
            post_pages = tool.paging_cursor_read_pages(
                cursor,
                pages=10,
                interval_seconds=0.5,
                batch_size=10,
                first_page_index=len(pre_pages),
            )
            logger.info("post-restart pages: %s", "; ".join(str(p) for p in post_pages))

            post_verdict = tool.verdict(post_pages)
            logger.info(f"post-restart verdict: {post_verdict.as_dict()}")

            # Deliverable assertion: cursor-lost surfaced. Plain transient_network
            # is informational here (envoy may flap during the restart), but
            # the load-bearing signal is the server saying "your cursor is
            # gone".
            assert post_verdict.cursor_lost > 0, (
                f"connectivity tool did not surface a cursor-lost failure after the "
                f"mongot pod was restarted. Verdict: {post_verdict.as_dict()}. "
                f"Either the cursor's mongot-side state survived the pod restart "
                f"(which would mean the test isn't actually testing what we think "
                f"it is), or the cursor-lost signal pattern surfaced as something "
                f"``classify_failure`` doesn't yet recognise — extend the regex to "
                f"cover this code path."
            )
        finally:
            try:
                cursor.close()
            except Exception:  # pragma: no cover — cleanup best-effort
                logger.debug("cursor.close() raised on cleanup; cursor may already be dead")
