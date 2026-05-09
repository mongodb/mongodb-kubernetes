"""E2E test for the search connectivity tool (KUBE-17).

Drives ``SearchConnectivityTool`` against a single-cluster managed-LB
MongoDBSearch deployment and proves the cache-distinguishing logic actually
works — by taking mongot down mid-paging via the operator and asserting the
tool surfaces the resulting connectivity errors rather than reporting a
green ``upstream_alive`` verdict.

The deployment scaffolding is delegated to
``tests.common.search.connectivity_bootstrap`` so this file can stay focused
on the connectivity-tool assertions themselves.
"""

from __future__ import annotations

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
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.rs_search_helper import get_rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.sharded_search_helper import create_issuer_ca

logger = test_logger.get_test_logger(__name__)

# User credentials — same shape as the existing managed-LB test fixtures.
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

ENVOY_PROXY_PORT = 27028

MDB_RESOURCE_NAME = "mdb-rs-conn-tool"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
RS_MEMBERS = 3

MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"


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


# ---------------------------------------------------------------------------
# Cluster bootstrap — thin pytest shells over connectivity_bootstrap helpers.
# ---------------------------------------------------------------------------


@mark.e2e_search_connectivity_tool
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    bootstrap.install_operator(namespace, operator_installation_config)


@mark.e2e_search_connectivity_tool
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    bootstrap.create_ops_manager(namespace)


@mark.e2e_search_connectivity_tool
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    bootstrap.install_tls_certificates(helper, issuer, RS_MEMBERS)


@mark.e2e_search_connectivity_tool
def test_create_database_resource(mdb: MongoDB):
    bootstrap.create_database_resource(mdb)


@mark.e2e_search_connectivity_tool
def test_create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
    mdb: MongoDB,
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


@mark.e2e_search_connectivity_tool
def test_deploy_lb_certificates(namespace: str, issuer: str):
    bootstrap.deploy_lb_certificates(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_connectivity_tool
def test_create_search_tls_certificate(namespace: str, issuer: str):
    bootstrap.create_search_tls_certificate(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_connectivity_tool
def test_create_search_resource(mdbs: MongoDBSearch):
    bootstrap.create_search_resource(mdbs)


@mark.e2e_search_connectivity_tool
def test_verify_envoy_deployment(namespace: str):
    bootstrap.verify_envoy_deployment(namespace, MDBS_RESOURCE_NAME)


@mark.e2e_search_connectivity_tool
def test_wait_for_database_ready(mdb: MongoDB):
    bootstrap.wait_for_database_ready(mdb)


@mark.e2e_search_connectivity_tool
def test_verify_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    bootstrap.verify_mongod_parameters(namespace, MDB_RESOURCE_NAME, RS_MEMBERS, mdbs.name, ENVOY_PROXY_PORT)


@mark.e2e_search_connectivity_tool
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_connectivity_tool
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    bootstrap.restore_sample_database(mdb, tools_pod, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)


@mark.e2e_search_connectivity_tool
def test_create_search_index(mdb: MongoDB):
    bootstrap.create_search_index(mdb, USER_NAME, USER_PASSWORD)


# ---------------------------------------------------------------------------
# Connectivity tool tests — the actual KUBE-17 deliverable.
# ---------------------------------------------------------------------------


@mark.e2e_search_connectivity_tool
def test_oneshot_search_succeeds_and_reports_upstream(mdb: MongoDB):
    """One-shot search with cache-busted query — must reach mongot."""
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    tool = SearchConnectivityTool(search_tester)

    result = tool.oneshot_search()
    logger.info(f"oneshot_search result: {result}")
    assert result.success, f"one-shot search failed: {result.error_class} {result.error_message}"
    assert result.returned_count > 0, "expected some results from cache-busted compound query"
    # The cache-busted query has never been served by mongot before, so the
    # result MUST come from upstream. If the latency band misclassifies it the
    # threshold is wrong — fail loudly so we tune it before relying on the
    # heuristic in availability tests.
    assert result.cache_hit_hint is False, (
        f"cache-busted oneshot query reported cache_hit_hint={result.cache_hit_hint}; "
        f"latency was {result.latency_ms:.1f}ms (threshold "
        f"{tool.cache_latency_threshold_ms}ms)."
    )

    verdict = tool.verdict([result])
    assert verdict.upstream_alive, f"verdict.upstream_alive should be True; got {verdict.as_dict()}"


@mark.e2e_search_connectivity_tool
def test_paging_search_first_page_is_upstream(mdb: MongoDB):
    """First paging page corresponds to the cursor's firstBatch — upstream."""
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    tool = SearchConnectivityTool(search_tester)

    pages = tool.paging_search(pages=3, interval_seconds=0.1, batch_size=20)
    logger.info("paging_search results: %s", "; ".join(str(p) for p in pages))
    assert pages, "paging_search returned no pages"
    assert pages[0].success, f"first page failed: {pages[0].error_class} {pages[0].error_message}"
    assert pages[0].cache_hit_hint is False, f"first page should always be upstream-confirmed; got {pages[0]}"
    assert pages[0].returned_count > 0, "first page returned 0 docs"


@mark.e2e_search_connectivity_tool
def test_paging_through_mongot_outage_surfaces_connectivity_error(mdb: MongoDB, mdbs: MongoDBSearch):
    """Cache-distinguishing assertion — the deliverable signal of KUBE-17.

    Open a paging cursor against a healthy mongot, then scale the
    MongoDBSearch CR to 0 replicas via the operator and continue paging.
    The connectivity tool must not report a green ``upstream_alive``
    verdict for pages served after mongot is gone, AND must surface a real
    connectivity-class error from at least one post-outage page —
    cache-only success on its own is not a useful signal (it tells us
    about the cursor's local buffer state, not about upstream availability).

    NOTE: this test only exercises the "no healthy upstream" path produced
    by taking all mongots away via the operator. The "lost long-living
    cursor" path (mongot/envoy/mongod restarts mid-cursor) is intentionally
    out of scope here and will land in a follow-up KUBE ticket.
    """
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    tool = SearchConnectivityTool(
        search_tester,
        # Loosen the threshold a bit because we're doing 1-doc-per-pull
        # iteration on getMore boundaries which can be jittery; the
        # buffer-probe heuristic is the load-bearing signal here, not
        # latency.
        cache_latency_threshold_ms=10.0,
    )

    # Open a cursor while mongot is healthy and confirm the heuristic
    # produces at least one upstream-confirmed page. Two pages with a
    # small batch is enough to cross at least one getMore boundary.
    pre_pages = tool.paging_search(pages=2, interval_seconds=0.1, batch_size=10)
    logger.info("pre-outage pages: %s", "; ".join(str(p) for p in pre_pages))
    assert any(p.success and p.cache_hit_hint is False for p in pre_pages), (
        "expected at least one upstream-confirmed page before scaling mongot down; "
        "the cache-detection heuristic is broken before we even introduce a fault"
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
        sts = apps_v1.read_namespaced_stateful_set(statefulset_name, mdb.namespace)
        ready = sts.status.ready_replicas or 0
        return ready == 0, f"ready_replicas={ready}"

    run_periodically(
        mongot_pods_gone,
        timeout=180,
        sleep_time=5,
        msg=f"mongot StatefulSet {statefulset_name} to scale to 0",
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

    # Deliverable assertion 1: the verdict cannot claim upstream is alive.
    assert post_verdict.upstream_succeeded == 0, (
        f"connectivity tool reported {post_verdict.upstream_succeeded} upstream-confirmed "
        f"successes after mongot scaled to 0 — the cache-distinguishing logic "
        f"is producing false-greens. Verdict: {post_verdict.as_dict()}"
    )
    # Deliverable assertion 2: at least one connectivity error must surface.
    # Cache-only success on its own is not informative — see the reviewer's
    # note on PR #1080. We need a real failure to know the tool is
    # propagating upstream-loss instead of silently swallowing it.
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


@mark.e2e_search_connectivity_tool
def test_paging_through_mongot_pod_restart_surfaces_lost_cursor(mdb: MongoDB, mdbs: MongoDBSearch):
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
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
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
        assert any(p.success and p.cache_hit_hint is False for p in pre_pages), (
            "expected at least one upstream-confirmed pre-restart page; " "cursor isn't actually contacting mongot"
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
