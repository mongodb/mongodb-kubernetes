"""Search availability under envoy LB scaling, driven through spec.loadBalancer.managed.replicas.

mongot scale up/down is covered in search_connectivity_tool.py. Here we scale the managed envoy
LB through its CR field and let the operator reconcile (no disable-reconciliation poking of the
Deployment). Scale up is purely additive -> no outage. Scale down shrinks the endpoint set, so new
queries recover while an established cursor pinned to a removed endpoint may drop; the deterministic
cursor-fault claim lives in search_availability_rolling_restart.py, which replaces every envoy pod.
"""

from __future__ import annotations

import pytest
from kubetester import list_matching_pods, pod_is_ready
from kubetester.kubetester import run_periodically
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester, assert_no_outage
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBRsDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchRsDeploymentTests,
    SearchSampleDataAndIndexTests,
)
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.rs_search_helper import rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_availability_envoy_scale

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-avail-escale")
SEARCH = SearchDeploymentConfig()
MDBS_NAME = MDB.mdb_resource_name
ENVOY_DEPLOYMENT = search_resource_names.lb_deployment_name(MDBS_NAME)
ENVOY_SELECTOR = f"app={ENVOY_DEPLOYMENT}"

BASELINE_OPS = 30
POST_EVENT_OPS = 15


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


def _assert_steady(namespace: str) -> None:
    tool = _user_tool(namespace)
    tool.wait_for_sentinel_indexed(timeout=300)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(tool, mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


def _wait_envoy_exact(namespace: str, replicas: int, timeout: int = 300) -> None:
    """Wait for EXACTLY ``replicas`` envoy pods, all Ready. wait_for_pods_ready is a >= lower
    bound, so it can't confirm a scale-down actually shed the surplus pod (which is the property
    the scale-down case asserts)."""

    def check() -> tuple:
        pods = list_matching_pods(namespace, label_selector=ENVOY_SELECTOR)
        ready = [p for p in pods if pod_is_ready(p)]
        return (
            len(pods) == replicas and len(ready) == replicas,
            f"envoy pods={len(pods)} ready={len(ready)} want={replicas}",
        )

    run_periodically(check, timeout=timeout, sleep_time=3, msg=f"envoy fleet == {replicas}")


def _scale_envoy(namespace: str, replicas: int) -> None:
    """Scale the managed envoy LB through its CR field and wait for the operator to settle it."""
    mdbs = _load_mdbs(namespace)
    mdbs["spec"]["loadBalancer"]["managed"]["replicas"] = replicas
    mdbs.update()
    logger.info(f"scaled managed envoy LB {ENVOY_DEPLOYMENT} -> replicas={replicas} (via CR)")
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    _wait_envoy_exact(namespace, replicas)


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


# --- scenario: envoy scale up then down -----------------------------------


class TestEnvoyScaling:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_envoy_scale_up_additive_no_outage(self, namespace: str):
        """Scale up is additive: new queries must not fail; open-cursor paging is observational (buffer-masked)."""
        up = SEARCH.envoy_lb_replicas + 1
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            _scale_envoy(namespace, up)
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
        logger.info(f"envoy-scale-up oneshot verdict: {oneshot.verdict.as_dict()}")
        logger.info(f"envoy-scale-up paging verdict (observational): {paging.verdict.as_dict()}")
        assert_no_outage(oneshot.verdict)
        _assert_steady(namespace)

    def test_envoy_scale_down_recovers(self, namespace: str):
        """Scale down shrinks the endpoint set: new queries recover. An established cursor pinned to a
        removed endpoint may drop -> log-only (deterministic cursor-fault coverage is in the
        rolling-restart suite). Restores the configured baseline for the trailing steady-state gate."""
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            _scale_envoy(namespace, 1)
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
        logger.info(f"envoy-scale-down oneshot verdict: {oneshot.verdict.as_dict()}")
        logger.info(f"envoy-scale-down paging verdict (observational): {paging.verdict.as_dict()}")
        # shedding an endpoint must not fail new queries; pinned-cursor paging is observational
        assert_no_outage(oneshot.verdict)
        _scale_envoy(namespace, SEARCH.envoy_lb_replicas)
        _assert_steady(namespace)
