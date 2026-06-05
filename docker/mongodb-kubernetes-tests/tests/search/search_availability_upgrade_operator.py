"""Search availability across operator-driven upgrades, on a managed-LB deployment.

The operator-version upgrade paths. This change is test-only, so the base-of-patch operator binary
is identical to this build — the upgrade is therefore exercised as a bundled-image change on the
same dev operator: install pinned to older mongot/envoy images (``search.version`` /
``search.envoyImage`` Helm values), then helm-upgrade back to the build's default images so the
operator rolls the data plane.

Two paths, both on a managed-LB deployment (multi mongot + multi envoy) so the open cursor rides
through and both roll counts are meaningful:
  - default-image-bump: the bundled mongot/envoy images change, the data plane rolls, and the open
    cursor serves fresh pages after recovery. Asserts ride-through plus a disruption bound.
  - no-image-bump: the operator is restarted with the bundled images held; the data plane should not
    roll. Measures the gratuitous-roll count (expected to fall to zero once the operator stops
    re-templating across restarts) and asserts continuous availability — it does not yet hard-assert
    zero rolls.

The MCK chart-version upgrade path is deferred: no released chart carries the managed-LB/envoy
feature yet, so a real chart-version upgrade is only constructible once the feature ships. The
released-operator -> dev single-mongot upgrade is covered in tests/upgrades/operator_upgrade_search.py.

Both paths drive an in-cluster operator (the helm-upgrade/restart must reconcile to take effect), so
the scenarios run only in CI and skip when the operator runs out-of-cluster (local `make run`,
Deployment scaled to 0). The deploy chain itself runs anywhere.
"""

from __future__ import annotations

import os
import time
from datetime import datetime, timezone

import pytest
from kubernetes import client
from kubetester import run_periodically
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
from tests.common.search.upgrade_availability import (
    assert_rolled_through,
    emit_metric,
    pod_uids,
    roll_count,
)
from tests.conftest import get_default_operator, get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_availability_upgrade_operator

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-upgrade-op")
SEARCH = SearchDeploymentConfig()
MDBS_NAME = MDB.mdb_resource_name
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"
ENVOY_DEPLOYMENT = search_resource_names.lb_deployment_name(MDBS_NAME)
ENVOY_SELECTOR = f"app={ENVOY_DEPLOYMENT}"

BASELINE_OPS = 30
POST_EVENT_OPS = 15

# Generous guard on the default-image-bump disruption; the measured value (SEARCH_UPGRADE_METRIC) is
# what the disruption-bound story cites — this only fails a pathological run.
DISRUPTION_BOUND_S = 300.0

# Older bundled images to install the "from" operator with, so the upgrade back to the build's
# defaults rolls the data plane. ``search.version`` is the mongot image tag; ``search.envoyImage``
# the full envoy image. Unset -> the default-image-bump scenario skips (no older image to bump from);
# envoy unset -> only mongot bumps (envoy roll count stays 0). CI supplies these.
OPERATOR_FROM_MONGOT_VERSION = os.getenv("OPERATOR_FROM_MONGOT_VERSION")
OPERATOR_FROM_ENVOY_IMAGE = os.getenv("OPERATOR_FROM_ENVOY_IMAGE")


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


def _operator_deployment(namespace: str):
    """The in-cluster operator Deployment, or None when the operator runs out-of-cluster (local
    `make run`, no Deployment). Found by name so we don't hardcode a Helm label that can drift."""
    for d in client.AppsV1Api().list_namespaced_deployment(namespace).items:
        if "operator" in d.metadata.name:
            return d
    return None


def _skip_unless_operator_in_cluster(namespace: str):
    dep = _operator_deployment(namespace)
    if dep is None or (dep.spec.replicas or 0) == 0:
        pytest.skip("operator runs out-of-cluster (Deployment absent or scaled to 0); exercised in CI")
    return dep


def _restart_operator(namespace: str) -> None:
    """Roll the operator pod without changing bundled images, via a restartedAt annotation, and wait
    for the new pod to be available — so a subsequent reconcile re-templates the data plane."""
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
        d = apps.read_namespaced_deployment(name, namespace)
        spec = d.spec.replicas or 0
        updated = d.status.updated_replicas or 0
        available = d.status.available_replicas or 0
        return updated >= spec and available >= spec, f"updated={updated} available={available} spec={spec}"

    run_periodically(rolled, timeout=180, sleep_time=3, msg=f"operator {name} restart")


def _assert_steady(namespace: str) -> None:
    """Recovery/steady-state gate: a clean window on both query types."""
    tool = _user_tool(namespace)
    tool.wait_for_sentinel_indexed(timeout=300)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(tool, mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


# --- deploy chain (once per file, on the "from" operator) -----------------


class TestInstallOperator(InstallOperatorTests):
    def test_install_operator(self, namespace: str, operator_installation_config: dict[str, str]):
        # Install pinned to the older bundled images so the later upgrade to the build defaults rolls
        # the data plane. In-cluster (CI) this sets the operator's images; out-of-cluster the env is
        # ignored by the host `make run` operator, so the deploy proceeds on its defaults.
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


# --- scenario: operator default-image-bump --------------------------------


class TestOperatorDefaultImageBump:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_default_image_bump_availability(self, namespace: str, operator_installation_config: dict[str, str]):
        """Helm-upgrade the operator back to the build's default bundled images: the mongot
        StatefulSet (and envoy Deployment, when an older envoy image was the source) rolls, the
        surviving replicas serve new queries, and the open cursor serves fresh pages after recovery."""
        _skip_unless_operator_in_cluster(namespace)
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
            t0 = time.monotonic()
            # No image overrides -> the chart's default (newer) bundled images.
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


# --- scenario: operator no-image-bump (gratuitous-roll measurement) -------


class TestOperatorNoImageBump:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_no_image_bump_availability(self, namespace: str):
        """Restart the operator with the bundled images held: a well-behaved operator re-templates
        the data plane identically and rolls nothing. Measure the data-plane roll count (the
        gratuitous-roll baseline) and assert continuous availability across the restart."""
        _skip_unless_operator_in_cluster(namespace)
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.1)
        with oneshot:
            oneshot.wait_for_operations(BASELINE_OPS)
            mongot_before = pod_uids(namespace, MONGOT_SELECTOR)
            envoy_before = pod_uids(namespace, ENVOY_SELECTOR)
            _restart_operator(namespace)
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
