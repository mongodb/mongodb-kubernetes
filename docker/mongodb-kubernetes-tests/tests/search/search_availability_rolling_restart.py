"""Search availability across rolling restarts of the data-plane Deployments/StatefulSets.

mongot/envoy kill and mongot scale are covered in search_connectivity_tool.py. Here we roll
envoy then mongot. Each scenario runs a background-tester window (new-query availability +
cursor ride-through) and a paging_baseline_and_fault drained sub-check, with a steady-state
gate between scenarios.
"""

from __future__ import annotations

import datetime

import pytest
from kubernetes import client
from kubetester import list_matching_pods
from kubetester.mongodb_search import MongoDBSearch
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import (
    SearchAvailabilityBackgroundTester,
    assert_no_outage,
    assert_outage_detected,
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
    paging_baseline_and_fault,
    wait_for_all_pods_replaced,
    wait_for_pods_by_label_replaced,
)
from tests.common.search.rs_search_helper import rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_availability_rolling_restart

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-avail-roll")
SEARCH = SearchDeploymentConfig()
# Search CR name defaults to the source MongoDB name; its index-0 mongot STS + envoy Deployment.
MDBS_NAME = MDB.mdb_resource_name
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"
ENVOY_DEPLOYMENT = search_resource_names.lb_deployment_name(MDBS_NAME)
ENVOY_SELECTOR = f"app={ENVOY_DEPLOYMENT}"

BASELINE_OPS = 30  # comfortably above assert_no_outage's min_operations=5 floor
POST_EVENT_OPS = 15


# --- shared helpers -------------------------------------------------------


def _user_tool(namespace: str) -> SearchConnectivityTool:
    """A fresh tool (own pymongo client) per call — never share one across concurrent testers."""
    return SearchConnectivityTool(rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password))


def _load_mdbs(namespace: str) -> MongoDBSearch:
    """The MongoDBSearch CR handle (mirrors search_connectivity_tool._load_mdbs)."""
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


def _pod_uids(namespace: str, label_selector: str) -> dict[str, str]:
    return {p.metadata.name: p.metadata.uid for p in list_matching_pods(namespace, label_selector=label_selector)}


def _rollout_restart(namespace: str, kind: str, name: str) -> None:
    """Bump the pod-template restartedAt annotation (kubectl rollout restart equivalent)."""
    stamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
    patch = {"spec": {"template": {"metadata": {"annotations": {"kubectl.kubernetes.io/restartedAt": stamp}}}}}
    apps = client.AppsV1Api()
    if kind == "deployment":
        apps.patch_namespaced_deployment(name=name, namespace=namespace, body=patch)
    else:
        apps.patch_namespaced_stateful_set(name=name, namespace=namespace, body=patch)
    logger.info(f"rollout-restart {kind}/{name}")


def _rollout_and_wait(namespace: str, kind: str, name: str, selector: str) -> None:
    """Rollout-restart the workload and wait for its pods to be fully replaced. Wait for as many
    new pods as were live before the roll (the actual replica count, not an assumed one).
    Deployments get fresh pod names (match by uid set); StatefulSets keep names (uid change)."""
    uids = _pod_uids(namespace, selector)
    _rollout_restart(namespace, kind, name)
    if kind == "deployment":
        wait_for_pods_by_label_replaced(namespace, selector, uids)
    else:
        wait_for_all_pods_replaced(namespace, uids)


def _assert_steady(namespace: str) -> None:
    """Recovery/steady-state gate: a clean window on both query types."""
    tool = _user_tool(namespace)
    tool.wait_for_sentinel_indexed(timeout=300)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(tool, mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


def _drained_cursor_subcheck(namespace: str, fault_fn, context: str) -> None:
    """Sub-check: force-drain a paging cursor past mongod's buffer through the fault. Full pod
    replacement RSTs the pinned mongod->mongot stream, so cursor_lost is deterministic here (vs the
    timing-dependent bg window). Mirrors search_connectivity_tool's envoy-restart assertion."""
    _, _, verdict = paging_baseline_and_fault(_user_tool(namespace), fault_fn=fault_fn)
    logger.info(f"{context} drained sub-check verdict: {verdict.as_dict()}")
    assert_outage_detected(verdict, accept_classes=("cursor_lost",))


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


# --- scenario: envoy Deployment rolling restart ---------------------------


class TestEnvoyRollingRestart:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_envoy_rolling_restart_availability(self, namespace: str):
        """Envoy roll: new queries stay available; cursor ride-through logged, hard-asserted in the drained sub-check."""
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            _rollout_and_wait(namespace, "deployment", ENVOY_DEPLOYMENT, ENVOY_SELECTOR)
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
        logger.info(f"envoy-roll oneshot verdict: {oneshot.verdict.as_dict()}")
        logger.info(f"envoy-roll paging verdict (observational): {paging.verdict.as_dict()}")
        # rolling-update keeps a ready endpoint (maxUnavailable=0 here), so no new query fails
        assert_no_outage(oneshot.verdict)
        _assert_steady(namespace)

    def test_envoy_rolling_restart_drained_cursor_fault(self, namespace: str):
        """(b) drained sub-check: envoy roll RSTs the pinned stream -> cursor_lost observable."""

        def fault() -> None:
            _rollout_and_wait(namespace, "deployment", ENVOY_DEPLOYMENT, ENVOY_SELECTOR)

        _drained_cursor_subcheck(namespace, fault, "envoy-roll")

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)


# --- scenario: mongot StatefulSet rolling restart -------------------------


class TestMongotRollingRestart:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_mongot_rolling_restart_availability(self, namespace: str):
        """Mongot roll: new queries may blip (transient_network) but hit no unexpected class; cursor ride-through logged, hard-asserted in the drained sub-check."""
        sts = search_resource_names.mongot_statefulset_name_for_cluster(MDBS_NAME)
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            _rollout_and_wait(namespace, "statefulset", sts, MONGOT_SELECTOR)
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
        logger.info(f"mongot-roll oneshot verdict: {oneshot.verdict.as_dict()}")
        logger.info(f"mongot-roll paging verdict (observational): {paging.verdict.as_dict()}")
        # transient blips OK while a replica cycles; recovery gated by trailing _assert_steady
        assert (
            oneshot.verdict.other_failed == 0 and oneshot.verdict.cursor_lost == 0
        ), f"mongot-roll: unexpected new-query failure class; {oneshot.verdict.as_dict()}"
        _assert_steady(namespace)

    def test_mongot_rolling_restart_drained_cursor_fault(self, namespace: str):
        """(b) drained sub-check: force-drain past the buffer through the roll -> cursor_lost observable."""
        sts = search_resource_names.mongot_statefulset_name_for_cluster(MDBS_NAME)

        def fault() -> None:
            _rollout_and_wait(namespace, "statefulset", sts, MONGOT_SELECTOR)

        _drained_cursor_subcheck(namespace, fault, "mongot-roll")

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)
