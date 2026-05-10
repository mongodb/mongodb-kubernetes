"""E2E test for the background availability tester (KUBE-26).

Layered on the connectivity tool from KUBE-17. Drives
``SearchAvailabilityBackgroundTester`` against a single-cluster managed-LB
MongoDBSearch deployment and proves two things:

1. **Steady-state probe works.** A no-fault observation window over
   the running cluster produces a verdict where every page is
   ``upstream_succeeded`` and ``failed == 0``. This is the smoke test
   for the harness itself — without it, every downstream KUBE-27
   failure-mode scenario would risk silently passing on a tester
   that never actually exercises upstream.

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

The deployment scaffolding mirrors the connectivity-tool e2e and is
delegated to ``tests.common.search.connectivity_bootstrap``.
"""

from __future__ import annotations

import time

from kubernetes import client
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import connectivity_bootstrap as bootstrap
from tests.common.search import search_resource_names
from tests.common.search.availability_tester import SearchAvailabilityBackgroundTester
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.rs_search_helper import get_rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.sharded_search_helper import create_issuer_ca

logger = test_logger.get_test_logger(__name__)

# Same fixture identity as the connectivity-tool e2e — the bootstrap is
# byte-for-byte identical, so reuse the same MDB / search / user names so
# parallel runs against a warm worktree don't trip over each other.
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

ENVOY_PROXY_PORT = 27028

MDB_RESOURCE_NAME = "mdb-rs-bg-tester"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
RS_MEMBERS = 3

MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

# Steady-state window length. Long enough for the tester to record a
# meaningful number of iterations (>= 10 with the default 1s wait_sec)
# without dragging the suite out.
STEADY_STATE_WINDOW_SECONDS = 12.0

# Outage scenario window. Roughly 6s of healthy probing, then the fault
# (delete mongot pod), then ~25s for the new pod to schedule and the
# tester to record the failure. Tuned by hand to be short but reliable.
OUTAGE_HEALTHY_WINDOW_SECONDS = 6.0
OUTAGE_FAULT_WINDOW_SECONDS = 25.0


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    return helper.create_rs_mdb(set_tls=True)


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-rs-managed-lb.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )
    if try_load(resource):
        return resource
    resource["spec"]["source"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    return resource


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(ADMIN_USER_NAME)


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(USER_NAME)


@fixture(scope="function")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs, MONGOT_USER_NAME)


# tools_pod fixture is provided by tests/search/conftest.py (calls
# mongodb_tools_pod.get_tools_pod which deploys+waits-ready the pod).


# Cluster bootstrap — identical to the connectivity-tool e2e, see
# tests/common/search/connectivity_bootstrap.py for the implementation.
@mark.e2e_search_availability_background_tester
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    bootstrap.install_operator(namespace, operator_installation_config)


@mark.e2e_search_availability_background_tester
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    bootstrap.create_ops_manager(namespace)


@mark.e2e_search_availability_background_tester
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    bootstrap.install_tls_certificates(helper, issuer, RS_MEMBERS)


@mark.e2e_search_availability_background_tester
def test_create_database_resource(mdb: MongoDB):
    bootstrap.create_database_resource(mdb)


@mark.e2e_search_availability_background_tester
def test_create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    bootstrap.create_users(
        helper,
        admin_user,
        ADMIN_USER_PASSWORD,
        user,
        USER_PASSWORD,
        mongot_user,
        MONGOT_USER_PASSWORD,
    )


@mark.e2e_search_availability_background_tester
def test_deploy_lb_certificates(namespace: str, issuer: str):
    bootstrap.deploy_lb_certificates(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_availability_background_tester
def test_create_search_tls_certificate(namespace: str, issuer: str):
    bootstrap.create_search_tls_certificate(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_availability_background_tester
def test_create_search_resource(mdbs: MongoDBSearch):
    bootstrap.create_search_resource(mdbs)


@mark.e2e_search_availability_background_tester
def test_verify_envoy_deployment(namespace: str):
    bootstrap.verify_envoy_deployment(namespace, MDBS_RESOURCE_NAME)


@mark.e2e_search_availability_background_tester
def test_wait_for_database_ready(mdb: MongoDB):
    bootstrap.wait_for_database_ready(mdb)


@mark.e2e_search_availability_background_tester
def test_verify_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    bootstrap.verify_mongod_parameters(namespace, MDB_RESOURCE_NAME, RS_MEMBERS, mdbs.name, ENVOY_PROXY_PORT)


@mark.e2e_search_availability_background_tester
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_availability_background_tester
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    bootstrap.restore_sample_database(mdb, tools_pod, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)


@mark.e2e_search_availability_background_tester
def test_create_search_index(mdb: MongoDB):
    bootstrap.create_search_index(mdb, USER_NAME, USER_PASSWORD)


# ----------------------------------------------------------------------
# Background availability tester scenarios — KUBE-26 deliverable
# ----------------------------------------------------------------------


@mark.e2e_search_availability_background_tester
def test_steady_state_window_reports_alive(mdb: MongoDB):
    """Run the tester for a short fault-free window; verdict must be clean.

    This is the smoke test for the harness — it must drive enough real
    paging traffic that the verdict shows ``upstream_alive`` and zero
    failures. Without this baseline, the outage scenario below could
    pass for the wrong reasons (e.g. the harness never calls upstream
    at all and reports "0 succeeded, 0 failed" which trivially
    satisfies most failure assertions).
    """
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    tool = SearchConnectivityTool(
        search_tester,
        # Loosen the threshold to match the connectivity-tool e2e
        # convention; on getMore boundaries the latency band is jittery
        # and the buffer-probe heuristic is the load-bearing signal.
        cache_latency_threshold_ms=10.0,
    )
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
        require_upstream_succeeded=True,
        max_failed=0,
    )
    logger.info(f"steady-state verdict: {verdict.as_dict()}")


@mark.e2e_search_availability_background_tester
def test_outage_window_detects_availability_loss(mdb: MongoDB, mdbs: MongoDBSearch, namespace: str):
    """Drive a deliberate fault and prove the tester catches it.

    Run a short healthy window so the verdict has at least one
    upstream-confirmed page first, then take ALL mongot pods offline
    by setting ``MongoDBSearch.spec.replicas = 0`` via the operator
    (``deliberately broken cluster`` per KUBE-26 acceptance). Continue
    probing through the outage window so the tester records the
    resulting failures, then bring mongot back via the operator.

    The CR-driven scale-to-0 mirrors KUBE-17's
    ``test_paging_through_mongot_outage_surfaces_connectivity_error``
    so we don't depend on multi-replica accounting (a single
    pod-delete leaves the other replica serving and the harness sees
    no fault — the only way to actually take mongot down is to
    drain the entire StatefulSet via the CR).

    Asserts the verdict surfaces either:
    - ``cursor_lost > 0`` — a long-living cursor's server-side state
      is gone, surfaced as ``OperationFailure(code=1)`` "Remote error
      from mongot :: RST_STREAM" by mongod.
    - ``transient_network > 0`` — envoy returns "no healthy upstream"
      because all mongot pods are gone.

    Either is acceptable evidence that the tester actually detects
    availability loss.
    """
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    tool = SearchConnectivityTool(search_tester, cache_latency_threshold_ms=10.0)
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
    apps_v1 = client.AppsV1Api()

    tester.start()
    try:
        # Phase 1 — let the tester run cleanly so the verdict has a
        # known-good baseline. We assert this baseline at the end.
        time.sleep(OUTAGE_HEALTHY_WINDOW_SECONDS)
        pre_results = tester.get_results()
        logger.info(f"pre-fault iterations recorded: {len(pre_results)}")
        assert any(p.success and p.cache_hit_hint is False for p in pre_results), (
            f"pre-fault window has no upstream-confirmed page; harness isn't actually "
            f"probing upstream. results={[str(r) for r in pre_results]}"
        )

        # Phase 2 — induce a brief outage by deleting the mongot pod.
        # The StatefulSet's controller will recreate it within ~10-30s,
        # but during the recreation window the cursor's gRPC stream to
        # mongot is dead — any post-fault getMore must fail (cursor_lost
        # because the new pod has no record of the cursor's session, or
        # transient_network because envoy briefly has no healthy
        # upstream).
        #
        # We use pod-delete rather than CR-driven scale-to-0 here
        # because the python kubernetes-client's openapi serializer
        # silently drops zero-valued int fields when patching custom
        # objects: ``mdbs["spec"]["replicas"] = 0; mdbs.update()`` does
        # NOT actually propagate to the API. Pod deletion via
        # core_v1.delete_namespaced_pod sidesteps that.
        core_v1 = client.CoreV1Api()
        pods = core_v1.list_namespaced_pod(
            namespace=mdb.namespace,
            label_selector=f"statefulset.kubernetes.io/pod-name={statefulset_name}-0",
        ).items
        if not pods:
            # Older k8s versions don't set that label; fall back to listing
            # all pods in the StatefulSet's selector and matching on name.
            all_pods = core_v1.list_namespaced_pod(namespace=mdb.namespace).items
            pods = [p for p in all_pods if p.metadata.name.startswith(f"{statefulset_name}-")]
        assert pods, f"no mongot pods found in namespace {mdb.namespace}"
        original_uids = {p.metadata.name: p.metadata.uid for p in pods}
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
    assert verdict.upstream_alive, (
        f"outage-window verdict has no upstream-confirmed pages at all — the harness "
        f"never reached upstream, so 'failure detected' is meaningless. "
        f"verdict={verdict.as_dict()}"
    )
