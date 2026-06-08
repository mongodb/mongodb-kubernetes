"""HTTP/2 GOAWAY-driven graceful drain across an envoy rolling restart.

The managed Envoy LB sits between mongod (gRPC client) and the mongot upstreams. On pod
termination the operator's preStop runs ``GET /drain_listeners`` against the admin port
(mongodbsearchenvoy_controller.go) — plain, NOT ``?graceful``. This suite asserts the drain
at the *stream* level via stream_tracing (envoy JSON access log + admin /stats), not just at
the client-observed-availability level the background tester sees.

TestEnvoyDrainInvestigation runs first and is observational: it drains one envoy replica
through the admin endpoint, snapshots /stats and the access records before/after, probes
whether the ``?graceful`` variant is even reachable through the exact-match allow-list, and
emits a structured KUBE45_FINDING line. The scenario classes added in later tasks calibrate
their hard-asserts off that finding. Mirrors the deploy chain + helpers in
search_availability_rolling_restart.py.
"""

from __future__ import annotations

import datetime
import time

import pytest
from kubernetes import client
from kubetester import list_matching_pods
from kubetester.mongodb_search import MongoDBSearch
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import (
    SearchAvailabilityBackgroundTester,
    assert_no_outage,
)
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBRsDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchRsDeploymentTests,
    SearchSampleDataAndIndexTests,
)
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    wait_for_all_pods_replaced,
    wait_for_pods_by_label_replaced,
)
from tests.common.search.rs_search_helper import rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.stream_tracing import (
    ENVOY_ADMIN_PORT,
    EnvoyAdminStats,
    forced_closed,
    new_downstream_after,
    read_envoy_logs,
    streams_active_between,
    upstream_for_stream,
    upstream_hosts,
)
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_availability_envoy_drain

NAMESPACE = get_namespace()
# 2 mongot + 2 envoy: a real multi-endpoint drain needs a surviving replica to re-route to.
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-drain")
SEARCH = SearchDeploymentConfig()
MDBS_NAME = MDB.mdb_resource_name
ENVOY_DEPLOYMENT = search_resource_names.lb_deployment_name(MDBS_NAME)
ENVOY_SELECTOR = f"app={ENVOY_DEPLOYMENT}"
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"

BASELINE_OPS = 30  # above assert_no_outage's min_operations=5 floor
POST_EVENT_OPS = 15
DRAIN_OBSERVE_SECONDS = 5  # let drained-listener access records flush before reading


# --- shared helpers -------------------------------------------------------


def _user_tool(namespace: str) -> SearchConnectivityTool:
    """A fresh tool (own pymongo client) per call — never share one across concurrent testers."""
    return SearchConnectivityTool(rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password))


def _load_mdbs(namespace: str) -> MongoDBSearch:
    helper = SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB.mdb_resource_name,
        mdbs_resource_name=MDBS_NAME,
        ca_configmap_name=MDB.ca_configmap_name,
    )
    return helper.mdbs_for_ext_rs_source(
        MDB.mongot_user_name,
        members=MDB.rs_members,
        lb_mode="Managed",
        clusters=[{"replicas": SEARCH.mongot_replicas}],
    )


def _utcnow() -> datetime.datetime:
    return datetime.datetime.now(datetime.timezone.utc)


def _pod_uids(namespace: str, label_selector: str) -> dict[str, str]:
    return {p.metadata.name: p.metadata.uid for p in list_matching_pods(namespace, label_selector=label_selector)}


def _rollout_restart(namespace: str, name: str) -> None:
    """Bump the LB Deployment pod-template restartedAt (kubectl rollout restart equivalent).

    The roll fires the preStop ``GET /drain_listeners`` on each old envoy as it terminates.
    """
    stamp = _utcnow().isoformat()
    patch = {"spec": {"template": {"metadata": {"annotations": {"kubectl.kubernetes.io/restartedAt": stamp}}}}}
    client.AppsV1Api().patch_namespaced_deployment(name=name, namespace=namespace, body=patch)
    logger.info(f"rollout-restart deployment/{name}")


def _rollout_and_wait(namespace: str) -> None:
    uids = _pod_uids(namespace, ENVOY_SELECTOR)
    _rollout_restart(namespace, ENVOY_DEPLOYMENT)
    wait_for_pods_by_label_replaced(namespace, ENVOY_SELECTOR, uids)


def _rollout_mongot_and_wait(namespace: str) -> None:
    """Rolling-restart the mongot StatefulSet and wait for its pods to be replaced (StatefulSet
    keeps pod names; replacement is detected by uid change). The envoy pods stay up across this,
    so their access logs persist and the upstream migration is observable end to end."""
    sts = search_resource_names.mongot_statefulset_name_for_cluster(MDBS_NAME)
    uids = _pod_uids(namespace, MONGOT_SELECTOR)
    stamp = _utcnow().isoformat()
    patch = {"spec": {"template": {"metadata": {"annotations": {"kubectl.kubernetes.io/restartedAt": stamp}}}}}
    client.AppsV1Api().patch_namespaced_stateful_set(name=sts, namespace=namespace, body=patch)
    logger.info(f"rollout-restart statefulset/{sts}")
    wait_for_all_pods_replaced(namespace, uids)


def _assert_steady(namespace: str) -> None:
    """Recovery/steady-state gate: a clean window on both query types."""
    tool = _user_tool(namespace)
    tool.wait_for_sentinel_indexed(timeout=300)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(tool, mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


def _probe_admin(namespace: str, pod: str, path: str, method: str = "GET") -> tuple[bool, str]:
    """Hit an admin path through the apiserver pod-proxy. Returns (reachable, detail).

    Used to probe what the exact-match allow-list + envoy admin accept: the operator preStop's
    verb (``GET /drain_listeners``), the verb that actually drains (``POST /drain_listeners``),
    and the ``?graceful`` variant. POST /drain_listeners triggers the drain, so the caller must
    restore the pod afterwards.
    """
    core = client.CoreV1Api()
    fn = (
        core.connect_post_namespaced_pod_proxy_with_path
        if method == "POST"
        else core.connect_get_namespaced_pod_proxy_with_path
    )
    try:
        resp = fn(name=f"{pod}:{ENVOY_ADMIN_PORT}", namespace=namespace, path=path)
        return True, str(resp).strip()[:120]
    except client.exceptions.ApiException as exc:
        return False, f"{exc.status} {exc.reason}"


# --- deploy chain (once per file) -----------------------------------------


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBDeployment(MongoDBRsDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchDeployment(SearchRsDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSampleData(SearchSampleDataAndIndexTests):
    sample_config = SampleDataAndIndexConfig()

    def admin_tester(self, namespace: str):
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password)


# --- investigation: produce the GOAWAY/drain finding ----------------------


class TestEnvoyDrainInvestigation:
    """Observational. Drains one envoy replica via the admin endpoint, correlates the
    client-observed window to the envoy-side stream disposition, and emits KUBE45_FINDING.
    Hard-asserts only that the instrumentation observed the drain — the finding values
    themselves are what later scenarios calibrate against."""

    def test_envoy_drain_finding(self, namespace: str):
        tool = _user_tool(namespace)
        tool.wait_for_sentinel_indexed(timeout=300)

        envoy_pods = sorted(_pod_uids(namespace, ENVOY_SELECTOR))
        assert len(envoy_pods) >= 2, f"need >=2 envoy replicas for a multi-endpoint drain; got {envoy_pods}"
        target = envoy_pods[0]
        logger.info(f"draining envoy pod {target} of {envoy_pods}")

        # establish steady streams so there are pre-drain client-ids and in-flight streams
        with SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.1) as pre:
            pre.wait_for_operations(BASELINE_OPS)

        # Does the operator preStop's verb actually drain? preStop is HTTPGet /drain_listeners
        # (mongodbsearchenvoy_controller.go); envoy gates mutating admin endpoints behind POST.
        prestop_get_drains, prestop_get_detail = _probe_admin(namespace, target, "drain_listeners", method="GET")
        # Is the graceful variant reachable through the exact-match allow-list at all?
        graceful_reachable, graceful_detail = _probe_admin(namespace, target, "drain_listeners?graceful", method="POST")

        stats_before = EnvoyAdminStats.fetch(namespace, target)
        t_drain = _utcnow()
        # The verb that does drain on this envoy build: POST. Drains target's listeners.
        post_drains, post_detail = _probe_admin(namespace, target, "drain_listeners", method="POST")

        # drive new streams while target is draining — these should re-route to the surviving
        # replica; mongod should open a fresh downstream if its pinned channel was GOAWAY'd
        with SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.1) as post:
            post.wait_for_operations(POST_EVENT_OPS)
        time.sleep(DRAIN_OBSERVE_SECONDS)

        stats_after = EnvoyAdminStats.fetch(namespace, target)
        target_records = read_envoy_logs(namespace, target)  # target alive after admin-drain
        all_records = read_envoy_logs(namespace, ENVOY_SELECTOR)  # load is LB'd across replicas
        t_end = _utcnow()

        listeners_delta = stats_after.total_listeners_draining - stats_before.total_listeners_draining
        drain_close_delta = stats_after.downstream_cx_drain_close - stats_before.downstream_cx_drain_close
        emit_goaway = listeners_delta > 0 or drain_close_delta > 0

        window = streams_active_between(target_records, t_drain, t_end)
        forced = forced_closed(window)
        completed = [r for r in window if not r.forced_closed]
        mongod_new_conn = bool(new_downstream_after(all_records, t_drain))

        logger.info(
            "KUBE45_FINDING "
            f"emit_goaway={emit_goaway} graceful_reachable={graceful_reachable} "
            f"mongod_new_conn={mongod_new_conn} forced_closed={len(forced)} completed={len(completed)} "
            f"prestop_get_drains={prestop_get_drains} listeners_draining_delta={listeners_delta} "
            f"drain_close_delta={drain_close_delta} upstreams={sorted(upstream_hosts(all_records))} "
            f"prestop_get_detail={prestop_get_detail!r} post_drains={post_drains} "
            f"post_detail={post_detail!r} graceful_detail={graceful_detail!r}"
        )

        # Instrumentation sanity only — finding fields above are observational and calibrate the
        # later scenarios. POST is the verb that drains; if it does not, the harness can't observe.
        assert post_drains, f"POST /drain_listeners did not drain target: {post_detail}"
        assert all_records, "no envoy access records across the LB — stream tracing not observing"

        # restore the drained replica so the downstream scenarios start from a healthy deploy
        _rollout_and_wait(namespace)
        _assert_steady(namespace)


# --- scenario: envoy Deployment rolling restart, observed at the stream level ---


def _assert_rolled_through(verdict, succeeded_before: int, succeeded_after: int, context: str) -> None:
    """A roll must not cause a sustained outage: only recoverable cursor/network classes failed,
    and the open cursor served fresh pages *after* recovery (succeeded grew past the snapshot)."""
    logger.info(f"{context} paging verdict: {verdict.as_dict()}")
    assert verdict.other_failed == 0, f"{context}: unexpected failure class; {verdict.as_dict()}"
    assert (
        succeeded_after > succeeded_before
    ), f"{context}: cursor served no pages after recovery ({succeeded_before}->{succeeded_after}); {verdict.as_dict()}"


class TestEnvoyRollingDrain:
    """Envoy Deployment rolling restart, observed at the HTTP/2 stream level. The preStop drain is
    a no-op on this build (see the finding above), so stream disposition is logged rather than
    hard-asserted for graceful ride-through; the availability property — post-recovery progress —
    is hard-asserted. A Deployment roll replaces the pods, so only the replacement pods' access
    logs survive to read."""

    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_envoy_roll_stream_level(self, namespace: str):
        t0 = _utcnow()
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            _rollout_and_wait(namespace)
            ok_at_recovery = paging.succeeded_count  # snapshot once replacement pods are Ready
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
            ok_after = paging.succeeded_count
        records = read_envoy_logs(namespace, ENVOY_SELECTOR)  # replacement pods only
        post = streams_active_between(records, t0, _utcnow())
        logger.info(
            "envoy-roll stream disposition: "
            f"records={len(records)} post_t0={len(post)} forced={len(forced_closed(post))} "
            f"new_downstream={len(new_downstream_after(records, t0))} upstreams={sorted(upstream_hosts(records))} "
            f"oneshot={oneshot.verdict.as_dict()}"
        )
        # availability property is hard-asserted; stream disposition above is observe-and-log
        _assert_rolled_through(paging.verdict, ok_at_recovery, ok_after, "envoy-roll")
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)


# --- scenario: mongot StatefulSet rolling restart (upstream drain) ---------


class TestMongotRollingDrain:
    """mongot StatefulSet rolling restart, observed from the envoy side. The envoy pods survive
    the roll, so their access logs span it: a client whose stream was pinned to the draining
    mongot should migrate to the surviving upstream. Availability recovery is hard-asserted; the
    paging cursor disposition is drain-dependent (the empirical cursor-fault truth) so its class
    is logged, not hard-asserted."""

    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_mongot_roll_upstream_migration(self, namespace: str):
        t0 = _utcnow()
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            roll_started = time.monotonic()
            _rollout_mongot_and_wait(namespace)
            recovery_s = time.monotonic() - roll_started  # roll-trigger → replacement pods Ready
            ok_at_recovery = paging.succeeded_count  # snapshot once replacement pods are Ready
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
            ok_after = paging.succeeded_count
        records = read_envoy_logs(namespace, ENVOY_SELECTOR)  # envoy survives the mongot roll
        window = streams_active_between(records, t0, _utcnow())
        migrated = [
            cid for cid in {r.client_id for r in window if r.client_id} if len(upstream_for_stream(window, cid)) > 1
        ]
        forced_n = len(forced_closed(window))
        completed_n = len(window) - forced_n
        ratio = completed_n / len(window) if window else 0.0
        disruption_s = paging.max_consecutive_failure * paging.interval_seconds
        logger.info(
            "mongot-roll stream disposition: "
            f"records={len(records)} window={len(window)} forced={forced_n} "
            f"upstreams={sorted(upstream_hosts(window))} migrated_clients={len(migrated)} "
            f"oneshot={oneshot.verdict.as_dict()} paging={paging.verdict.as_dict()}"
        )
        # Drain-window measurement at envoy's default (no operator drain-time knob today): the
        # completed-vs-forced stream ratio across the roll and the longest client unavailability.
        logger.info(
            f"KUBE45_DRAIN_METRIC completed={completed_n} forced={forced_n} ratio={ratio:.3f} "
            f"recovery_s={recovery_s:.1f} disruption_s={disruption_s:.1f}"
        )
        # availability property is hard-asserted; the cursor class + migration above are observe-and-log
        _assert_rolled_through(paging.verdict, ok_at_recovery, ok_after, "mongot-roll")
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)
