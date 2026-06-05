"""Search availability across an operator-version upgrade, on a managed-LB deployment.

The real customer upgrade path: deploy on the latest released operator (single-cluster managed Envoy
LB shipped in MCK 1.8.0), then upgrade to this build. The upgrade is decomposed into its two
distinct effects on one managed-LB deployment (2 mongot + 2 envoy), so each is measured in isolation:

  - no-image-bump (first): upgrade to the dev operator with the bundled mongot/envoy images held to
    the released operator's values. Only the operator binary changes, so a well-behaved operator
    re-templates the data plane identically and rolls nothing. Measures the gratuitous-roll count
    (the real binary-version delta the released-operator source provides) and asserts continuous
    availability. No hard zero-roll assert yet — reported until the gratuitous-roll fix lands.
  - default-image-bump (second): upgrade the bundled images to the build's defaults. The data plane
    rolls; the open cursor rides through and serves fresh pages after recovery. Asserts ride-through
    plus a disruption bound.

Together (binary change + image change) these are the released-operator -> dev chart upgrade,
decomposed to attribute rolls to the binary vs the images. The atomic single-mongot variant lives in
tests/upgrades/operator_upgrade_search.py.

Both upgrades need in-cluster operators (released-then-dev), which is incompatible with the local
out-of-cluster `make run` operator, so the whole suite is EVG-only and skips when LOCAL_OPERATOR.
"""

from __future__ import annotations

import time

import pytest
from kubernetes import client
from kubetester import run_periodically
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester, assert_no_outage
from tests.common.search.bootstrap_test_mixins import (
    MongoDBDeploymentConfig,
    MongoDBRsDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchRsDeploymentTests,
    SearchSampleDataAndIndexTests,
)
from tests.common.search.connectivity import SearchConnectivityTool, wait_for_all_pods_replaced
from tests.common.search.rs_search_helper import rs_search_tester
from tests.common.search.upgrade_availability import (
    assert_rolled_through,
    emit_metric,
    pod_uids,
    roll_count,
)
from tests.conftest import get_default_operator, get_namespace, local_operator

logger = test_logger.get_test_logger(__name__)

pytestmark = [
    pytest.mark.e2e_search_availability_upgrade_operator,
    # Released-then-dev in-cluster operators are incompatible with the local `make run` operator.
    pytest.mark.skipif(local_operator(), reason="operator-version upgrade needs in-cluster operators (EVG-only)"),
]

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-upgrade-op")
SEARCH = SearchDeploymentConfig()
MDBS_NAME = MDB.mdb_resource_name
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"
ENVOY_DEPLOYMENT = search_resource_names.lb_deployment_name(MDBS_NAME)
ENVOY_SELECTOR = f"app={ENVOY_DEPLOYMENT}"

BASELINE_OPS = 30
POST_EVENT_OPS = 15

# Generous guard on the image-bump disruption; the measured value (SEARCH_UPGRADE_METRIC) is what the
# disruption-bound story cites — this only fails a pathological run.
DISRUPTION_BOUND_S = 300.0

# Operator env vars holding the bundled mongot version / envoy image (helm_chart search.version /
# search.envoyImage). Read off the released operator to hold them across the no-image-bump upgrade.
SEARCH_VERSION_ENV = "MDB_SEARCH_VERSION"
ENVOY_IMAGE_ENV = "MDB_ENVOY_IMAGE"


# --- shared helpers -------------------------------------------------------


def _user_tool(namespace: str) -> SearchConnectivityTool:
    """A fresh tool (own pymongo client) per call — never share one across concurrent testers."""
    return SearchConnectivityTool(rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password))


def _operator_deployment(namespace: str):
    """The in-cluster operator Deployment, found by name so we don't hardcode a Helm label."""
    for d in client.AppsV1Api().list_namespaced_deployment(namespace).items:
        if "operator" in d.metadata.name:
            return d
    raise AssertionError("operator Deployment not found")


def _operator_bundled_images(namespace: str) -> dict[str, str]:
    """The running operator's bundled mongot version + envoy image, read from its Deployment env, as
    Helm overrides — so an upgrade can hold the data-plane images exactly where they are."""
    env = {e.name: e.value for e in (_operator_deployment(namespace).spec.template.spec.containers[0].env or [])}
    overrides: dict[str, str] = {}
    if env.get(SEARCH_VERSION_ENV):
        overrides["search.version"] = env[SEARCH_VERSION_ENV]
    if env.get(ENVOY_IMAGE_ENV):
        overrides["search.envoyImage"] = env[ENVOY_IMAGE_ENV]
    return overrides


def _wait_for_operator_rolled(namespace: str) -> None:
    """Block until the operator Deployment has finished rolling to the new template."""

    def rolled() -> tuple[bool, str]:
        d = _operator_deployment(namespace)
        spec = d.spec.replicas or 0
        updated = d.status.updated_replicas or 0
        available = d.status.available_replicas or 0
        return (
            updated >= spec and available >= spec and spec > 0,
            f"updated={updated} available={available} spec={spec}",
        )

    run_periodically(rolled, timeout=300, sleep_time=3, msg="operator upgrade rollout")


def _assert_steady(namespace: str) -> None:
    """Recovery/steady-state gate: a clean window on both query types."""
    tool = _user_tool(namespace)
    tool.wait_for_sentinel_indexed(timeout=300)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(tool, mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


# --- deploy chain (once per file, on the released operator) ---------------


class TestInstallOperator:
    def test_install_official_operator(self, namespace: str, official_operator):
        # Latest released operator (single-cluster managed Envoy LB shipped in MCK 1.8.0).
        official_operator.assert_is_running()


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


# --- scenario: no-image-bump (operator binary change, images held) --------


class TestOperatorUpgradeNoImageBump:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_no_image_bump_availability(self, namespace: str, operator_installation_config: dict[str, str]):
        """Upgrade the released operator to this build with the bundled images held to the released
        operator's values. Only the binary changes — a well-behaved operator rolls nothing. Measure
        the data-plane roll count (the gratuitous-roll baseline) and assert continuous availability."""
        held_images = _operator_bundled_images(namespace)
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.1)
        with oneshot:
            oneshot.wait_for_operations(BASELINE_OPS)
            mongot_before = pod_uids(namespace, MONGOT_SELECTOR)
            envoy_before = pod_uids(namespace, ENVOY_SELECTOR)
            get_default_operator(
                namespace,
                operator_installation_config={**operator_installation_config, **held_images},
                apply_crds_first=True,
            ).assert_is_running()
            _wait_for_operator_rolled(namespace)
            oneshot.wait_for_operations(POST_EVENT_OPS)
        rolls_mongot = roll_count(namespace, MONGOT_SELECTOR, mongot_before)
        rolls_envoy = roll_count(namespace, ENVOY_SELECTOR, envoy_before)
        logger.info(f"operator-no-bump rolls: mongot={rolls_mongot} envoy={rolls_envoy} (target: 0 each)")
        emit_metric(
            "operator_no_image_bump",
            rolls_mongot=rolls_mongot,
            rolls_envoy=rolls_envoy,
            recovery_s=0.0,
            disruption_s=0.0,
        )
        assert_no_outage(oneshot.verdict)
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)


# --- scenario: default-image-bump (bundled images change) -----------------


class TestOperatorUpgradeImageBump:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_default_image_bump_availability(self, namespace: str, operator_installation_config: dict[str, str]):
        """Upgrade the bundled images to the build's defaults: the mongot StatefulSet and envoy
        Deployment roll, the surviving replicas serve new queries, and the open cursor serves fresh
        pages after recovery."""
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            mongot_before = pod_uids(namespace, MONGOT_SELECTOR)
            envoy_before = pod_uids(namespace, ENVOY_SELECTOR)
            t0 = time.monotonic()
            # No image overrides -> the chart's default (build) bundled images.
            get_default_operator(
                namespace, operator_installation_config=operator_installation_config, apply_crds_first=True
            ).assert_is_running()
            wait_for_all_pods_replaced(namespace, mongot_before)
            recovery_s = time.monotonic() - t0
            ok_at_recovery = paging.succeeded_count  # snapshot once pods are Ready again
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
            ok_after = paging.succeeded_count
        disruption_s = paging.max_consecutive_failure * paging.interval_seconds
        rolls_mongot = roll_count(namespace, MONGOT_SELECTOR, mongot_before)
        rolls_envoy = roll_count(namespace, ENVOY_SELECTOR, envoy_before)
        logger.info(f"operator-default-bump oneshot verdict: {oneshot.verdict.as_dict()}")
        emit_metric(
            "operator_default_image_bump",
            rolls_mongot=rolls_mongot,
            rolls_envoy=rolls_envoy,
            recovery_s=recovery_s,
            disruption_s=disruption_s,
        )
        assert_rolled_through(paging.verdict, ok_at_recovery, ok_after, "operator-default-bump")
        assert (
            disruption_s <= DISRUPTION_BOUND_S
        ), f"operator-default-bump: disruption {disruption_s:.1f}s exceeded bound {DISRUPTION_BOUND_S:.1f}s"
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)
