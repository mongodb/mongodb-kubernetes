"""Search availability across an operator-version upgrade, on a managed-LB deployment.

This change is test-only (no operator code change), so the base-of-patch operator binary equals this
build; the operator-version upgrade is exercised as a bundled-image change on the same dev operator
(install pinned to older mongot/envoy images via the ``search.version`` / ``search.envoyImage`` Helm
values, then helm-upgrade to the build defaults). It stays on the dev operator throughout because the
multi-mongot managed-LB CR API this topology uses (``spec.clusters``, ``loadBalancer.managed.replicas``)
is dev-only — the latest released CRD rejects it under strict validation — so a released -> dev upgrade
of *this* topology is not constructible. The released -> dev binary-delta upgrade is covered on the
released-compatible single-mongot CR in tests/upgrades/operator_upgrade_search.py.

Two paths on one managed-LB deployment (2 mongot + 2 envoy):
  - default-image-bump: helm-upgrade the operator's bundled images to the build defaults; mongot and
    envoy roll; the open cursor rides through and serves fresh pages after recovery. Asserts the roll
    happened, ride-through, and a disruption bound. Skips unless OPERATOR_FROM_MONGOT_VERSION supplies
    an older source tag (no older image -> no bump to measure).
  - no-image-bump: restart the operator with the bundled images held; measure the gratuitous-roll
    count and require availability to ride through (managed LB). Does not hard-assert zero rolls yet —
    reported until the gratuitous-roll fix lands.

EVG-only: the upgrade/restart must reconcile via an in-cluster operator, which is incompatible with the
local out-of-cluster `make run` operator, so the suite skips when LOCAL_OPERATOR.
"""

from __future__ import annotations

import os
import time
from datetime import datetime, timezone

import pytest
from kubernetes import client
from kubetester import get_statefulset, run_periodically
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
from tests.common.search.connectivity import SearchConnectivityTool, wait_for_all_pods_replaced
from tests.common.search.rs_search_helper import rs_search_tester
from tests.common.search.upgrade_availability import assert_rolled_through, emit_metric, pod_uids, roll_count
from tests.conftest import get_default_operator, get_namespace, local_operator

logger = test_logger.get_test_logger(__name__)

pytestmark = [
    pytest.mark.e2e_search_availability_upgrade_operator,
    # An in-cluster operator must reconcile the upgrade/restart; the local `make run` operator can't.
    pytest.mark.skipif(local_operator(), reason="operator-version upgrade needs an in-cluster operator (EVG-only)"),
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

# Sustained-outage ceiling for a managed-LB ride-through; the surviving replica serves through a
# one-at-a-time roll, so the real value is small. Fails a pathological run.
DISRUPTION_BOUND_S = 60.0

# Bundled images to install the "from" operator with, so the upgrade back to the build's defaults
# rolls the data plane. ``search.version`` is the mongot image tag (default differs from the build's
# bundled default, so the bump rolls mongot; override per-env); ``search.envoyImage`` the full envoy
# image, defaulted to a same-minor patch of the bundled envoy so the bump rolls envoy too without risk
# of a config-incompatible crashloop. Set either to an empty string to drop it from the bump.
OPERATOR_FROM_MONGOT_VERSION = os.getenv("OPERATOR_FROM_MONGOT_VERSION", "9872ab5089")
OPERATOR_FROM_ENVOY_IMAGE = os.getenv("OPERATOR_FROM_ENVOY_IMAGE", "envoyproxy/envoy:v1.37.0")


# --- shared helpers -------------------------------------------------------


def _user_tool(namespace: str) -> SearchConnectivityTool:
    """A fresh tool (own pymongo client) per call — never share one across concurrent testers."""
    return SearchConnectivityTool(rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password))


def _from_image_config(config: dict[str, str]) -> dict[str, str]:
    """A copy of the operator install config pinned to the older bundled images (the upgrade
    source). Returns the config unchanged when no older images are configured."""
    overrides: dict[str, str] = {}
    if OPERATOR_FROM_MONGOT_VERSION:
        overrides["search.version"] = OPERATOR_FROM_MONGOT_VERSION
    if OPERATOR_FROM_ENVOY_IMAGE:
        overrides["search.envoyImage"] = OPERATOR_FROM_ENVOY_IMAGE
    return {**config, **overrides} if overrides else config


def _mongot_image_tag(namespace: str) -> str:
    """The mongot container's image tag in the search StatefulSet."""
    sts = get_statefulset(namespace, search_resource_names.mongot_statefulset_name(MDBS_NAME))
    mongot = next(c for c in sts.spec.template.spec.containers if c.name == "mongot")
    return mongot.image.split(":")[-1]


def _operator_deployment(namespace: str):
    """The in-cluster operator Deployment, found by name so we don't hardcode a Helm label."""
    for d in client.AppsV1Api().list_namespaced_deployment(namespace).items:
        if "operator" in d.metadata.name:
            return d
    raise AssertionError("operator Deployment not found")


def _restart_operator(namespace: str) -> None:
    """Roll the operator pod without changing bundled images (restartedAt annotation) and wait for
    the new pod to be available, so a subsequent reconcile re-templates the data plane."""
    apps = client.AppsV1Api()
    name = _operator_deployment(namespace).metadata.name
    apps.patch_namespaced_deployment(
        name,
        namespace,
        {
            "spec": {
                "template": {
                    "metadata": {
                        "annotations": {"kubectl.kubernetes.io/restartedAt": datetime.now(timezone.utc).isoformat()}
                    }
                }
            }
        },
    )

    def rolled() -> tuple[bool, str]:
        d = _operator_deployment(namespace)
        spec = d.spec.replicas or 0
        updated = d.status.updated_replicas or 0
        available = d.status.available_replicas or 0
        return (
            updated >= spec and available >= spec and spec > 0,
            f"updated={updated} available={available} spec={spec}",
        )

    run_periodically(rolled, timeout=180, sleep_time=3, msg=f"operator {name} restart")


def _assert_steady(namespace: str) -> None:
    """Recovery/steady-state gate: a clean window on both query types (fresh client per tester)."""
    _user_tool(namespace).wait_for_sentinel_indexed(timeout=300)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(_user_tool(namespace), mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


# --- deploy chain (once per file, on the dev operator pinned to older images) ---


class TestInstallOperator(InstallOperatorTests):
    def test_install_operator(self, namespace: str, operator_installation_config: dict[str, str]):
        # Pin the older bundled images so the later upgrade to the build defaults rolls the data plane.
        super().test_install_operator(namespace, _from_image_config(operator_installation_config))


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


# --- scenario: default-image-bump (bundled images change) -----------------


class TestOperatorDefaultImageBump:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_default_image_bump_availability(self, namespace: str, operator_installation_config: dict[str, str]):
        """Helm-upgrade the operator to the build's default bundled images: the mongot StatefulSet and
        envoy Deployment roll, the surviving replicas serve new queries, and the open cursor serves
        fresh pages after recovery."""
        if not OPERATOR_FROM_MONGOT_VERSION:
            pytest.skip("OPERATOR_FROM_MONGOT_VERSION not set; supply an older bundled mongot tag to bump from")
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            mongot_before = pod_uids(namespace, MONGOT_SELECTOR)
            envoy_before = pod_uids(namespace, ENVOY_SELECTOR)
            tag_before = _mongot_image_tag(namespace)
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
        tag_after = _mongot_image_tag(namespace)
        logger.info(f"operator-default-bump oneshot verdict: {oneshot.verdict.as_dict()}")
        emit_metric(
            "operator_default_image_bump",
            rolls_mongot=rolls_mongot,
            rolls_envoy=rolls_envoy,
            recovery_s=recovery_s,
            disruption_s=disruption_s,
        )
        # The bump actually happened (not a no-op): the mongot image changed and every mongot rolled.
        assert tag_after != tag_before, f"operator-default-bump was a no-op: mongot tag stayed {tag_before}"
        assert (
            rolls_mongot >= SEARCH.mongot_replicas
        ), f"expected all {SEARCH.mongot_replicas} mongot to roll, got {rolls_mongot}"
        # When the "from" install pinned an older envoy image, the bump back to the bundled default
        # must roll envoy too.
        if OPERATOR_FROM_ENVOY_IMAGE:
            assert rolls_envoy >= 1, f"envoy image bumped but no envoy pod rolled (rolls_envoy={rolls_envoy})"
        assert_rolled_through(
            paging.verdict,
            ok_at_recovery,
            ok_after,
            "operator-default-bump",
            disruption_s=disruption_s,
            bound_s=DISRUPTION_BOUND_S,
        )
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)


# --- scenario: no-image-bump (operator restart, images held) --------------


class TestOperatorNoImageBump:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_no_image_bump_availability(self, namespace: str):
        """Restart the operator with the bundled images held: a well-behaved operator re-templates
        the data plane identically and rolls nothing. Measure the data-plane roll count (the
        gratuitous-roll baseline) and require availability to ride through the restart."""
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.1)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            mongot_before = pod_uids(namespace, MONGOT_SELECTOR)
            envoy_before = pod_uids(namespace, ENVOY_SELECTOR)
            t0 = time.monotonic()
            _restart_operator(namespace)
            recovery_s = time.monotonic() - t0
            ok_at_recovery = paging.succeeded_count
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
            ok_after = paging.succeeded_count
        disruption_s = paging.max_consecutive_failure * paging.interval_seconds
        rolls_mongot = roll_count(namespace, MONGOT_SELECTOR, mongot_before)
        rolls_envoy = roll_count(namespace, ENVOY_SELECTOR, envoy_before)
        logger.info(f"operator-no-bump rolls: mongot={rolls_mongot} envoy={rolls_envoy} (target: 0 each)")
        emit_metric(
            "operator_no_image_bump",
            rolls_mongot=rolls_mongot,
            rolls_envoy=rolls_envoy,
            recovery_s=recovery_s,
            disruption_s=disruption_s,
        )
        # The roll count is measured (the gratuitous-roll baseline), not yet hard-asserted to be zero;
        # availability must hold across the restart whether or not a gratuitous roll occurs.
        assert_rolled_through(
            paging.verdict,
            ok_at_recovery,
            ok_after,
            "operator-no-bump",
            disruption_s=disruption_s,
            bound_s=DISRUPTION_BOUND_S,
        )
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)
