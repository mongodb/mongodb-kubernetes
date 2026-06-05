"""Search availability across data-plane version/image upgrades.

Two upgrade paths on one deployment: a mongot version upgrade driven through the
MongoDBSearch CR (``spec.version``), and an envoy image upgrade driven through the
operator's ``MDB_ENVOY_IMAGE``. Each runs a background-tester window across the upgrade
and asserts post-recovery progress (the open cursor serves fresh pages after the roll),
emitting a per-run roll count and recovery time. The envoy path needs an in-cluster
operator restart, so it runs only in CI; the mongot path reconciles a CR field and runs
anywhere. Operator/chart upgrades live in tests/upgrades/operator_upgrade_search.py.
"""

from __future__ import annotations

import os
import time

import pytest
from kubernetes import client
from kubetester import get_statefulset
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
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    wait_for_all_pods_replaced,
    wait_for_pods_by_label_replaced,
)
from tests.common.search.rs_search_helper import rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.upgrade_availability import assert_rolled_through as _assert_rolled_through
from tests.common.search.upgrade_availability import emit_metric as _emit_metric
from tests.common.search.upgrade_availability import pod_uids as _pod_uids
from tests.common.search.upgrade_availability import roll_count as _roll_count
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_availability_upgrade

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-avail-upgrade")
SEARCH = SearchDeploymentConfig()
MDBS_NAME = MDB.mdb_resource_name
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"
ENVOY_DEPLOYMENT = search_resource_names.lb_deployment_name(MDBS_NAME)
ENVOY_SELECTOR = f"app={ENVOY_DEPLOYMENT}"

BASELINE_OPS = 30
POST_EVENT_OPS = 15

# Sustained-outage ceiling for a managed-LB ride-through. The surviving replica serves through a
# one-at-a-time roll, so the real value is small; this fails a pathological run (was 300s — so loose
# it could never fire).
DISRUPTION_BOUND_S = 60.0

# mongot tag to upgrade to (used as the image tag via spec.version). Default is a published
# staging/mongodb-search manifest tag newer than the operator default; override per-env.
MONGOT_UPGRADE_VERSION = os.getenv("MONGOT_UPGRADE_VERSION", "9872ab5089")
# envoy image to upgrade to. Unset -> the envoy path skips (it needs an in-cluster operator
# restart to take effect, so it only runs in CI where a target image is supplied).
ENVOY_UPGRADE_IMAGE = os.getenv("ENVOY_UPGRADE_IMAGE")
# Operator env var that holds the managed-LB envoy image (pkg/util/constants.go EnvoyImageEnv).
ENVOY_IMAGE_ENV = "MDB_ENVOY_IMAGE"


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


def _mongot_image_tag(namespace: str) -> str:
    """The mongot container's image tag in the search StatefulSet."""
    sts = get_statefulset(namespace, search_resource_names.mongot_statefulset_name(MDBS_NAME))
    mongot = next(c for c in sts.spec.template.spec.containers if c.name == "mongot")
    return mongot.image.split(":")[-1]


def _assert_steady(namespace: str) -> None:
    """Recovery/steady-state gate: a clean window on both query types (fresh client per tester)."""
    _user_tool(namespace).wait_for_sentinel_indexed(timeout=300)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(_user_tool(namespace), mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


def _operator_deployment(namespace: str):
    """The in-cluster operator Deployment, or None when the operator runs out-of-cluster (local
    `make run`, no Deployment). Found by name so we don't hardcode a Helm label that can drift."""
    for d in client.AppsV1Api().list_namespaced_deployment(namespace).items:
        if "operator" in d.metadata.name:
            return d
    return None


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


# --- scenario: mongot version upgrade (CR spec.version) -------------------


class TestMongotVersionUpgrade:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_mongot_version_upgrade_availability(self, namespace: str):
        """Bump spec.version: the mongot StatefulSet rolls one pod at a time, the surviving
        replica serves new queries, and the open cursor serves fresh pages after recovery."""
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            mongot_before = _pod_uids(namespace, MONGOT_SELECTOR)
            envoy_before = _pod_uids(namespace, ENVOY_SELECTOR)
            tag_before = _mongot_image_tag(namespace)
            mdbs = _load_mdbs(namespace)
            mdbs.load()
            mdbs["spec"]["version"] = MONGOT_UPGRADE_VERSION
            t0 = time.monotonic()
            mdbs.update()
            wait_for_all_pods_replaced(namespace, mongot_before)
            mdbs.assert_reaches_phase(Phase.Running, timeout=600)
            recovery_s = time.monotonic() - t0
            ok_at_recovery = paging.succeeded_count  # snapshot once pods are Ready again
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
            ok_after = paging.succeeded_count
        disruption_s = paging.max_consecutive_failure * paging.interval_seconds
        rolls_mongot = _roll_count(namespace, MONGOT_SELECTOR, mongot_before)
        rolls_envoy = _roll_count(namespace, ENVOY_SELECTOR, envoy_before)
        tag_after = _mongot_image_tag(namespace)
        logger.info(f"mongot-version oneshot verdict: {oneshot.verdict.as_dict()}")
        _emit_metric(
            "mongot",
            rolls_mongot=rolls_mongot,
            rolls_envoy=rolls_envoy,
            recovery_s=recovery_s,
            disruption_s=disruption_s,
        )
        # The upgrade actually happened (not a silent no-op): every mongot rolled to the new tag,
        # and the mongot-only bump left envoy untouched.
        assert (
            tag_after == MONGOT_UPGRADE_VERSION and tag_after != tag_before
        ), f"mongot version upgrade was a no-op: tag {tag_before}->{tag_after}, target {MONGOT_UPGRADE_VERSION}"
        assert (
            rolls_mongot >= SEARCH.mongot_replicas
        ), f"expected all {SEARCH.mongot_replicas} mongot to roll, got {rolls_mongot}"
        assert rolls_envoy == 0, f"a mongot version bump must not disturb envoy, but {rolls_envoy} envoy pod(s) rolled"
        _assert_rolled_through(
            paging.verdict,
            ok_at_recovery,
            ok_after,
            "mongot-version",
            disruption_s=disruption_s,
            bound_s=DISRUPTION_BOUND_S,
        )
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)


# --- scenario: envoy image upgrade (operator MDB_ENVOY_IMAGE) -------------


class TestEnvoyImageUpgrade:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_envoy_image_upgrade_availability(self, namespace: str):
        """Change the operator's envoy image and restart it: the operator rolls the envoy
        Deployment to the new image. Needs an in-cluster operator restart to take effect, so it
        runs only in CI (skips when the operator runs out-of-cluster or no target image is set)."""
        dep = _operator_deployment(namespace)
        if dep is None or (dep.spec.replicas or 0) == 0:
            pytest.skip("operator runs out-of-cluster (Deployment absent or scaled to 0); exercised in CI")
        if not ENVOY_UPGRADE_IMAGE:
            pytest.skip("ENVOY_UPGRADE_IMAGE not set; supply a target envoy image to exercise this path")
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            envoy_before = _pod_uids(namespace, ENVOY_SELECTOR)
            t0 = time.monotonic()
            _set_operator_env(namespace, dep.metadata.name, ENVOY_IMAGE_ENV, ENVOY_UPGRADE_IMAGE)
            wait_for_pods_by_label_replaced(namespace, ENVOY_SELECTOR, envoy_before, expected=SEARCH.envoy_lb_replicas)
            recovery_s = time.monotonic() - t0
            ok_at_recovery = paging.succeeded_count
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
            ok_after = paging.succeeded_count
        disruption_s = paging.max_consecutive_failure * paging.interval_seconds
        rolls_envoy = _roll_count(namespace, ENVOY_SELECTOR, envoy_before)
        logger.info(f"envoy-image oneshot verdict: {oneshot.verdict.as_dict()}")
        _emit_metric("envoy", rolls_mongot=0, rolls_envoy=rolls_envoy, recovery_s=recovery_s, disruption_s=disruption_s)
        assert (
            rolls_envoy >= 1
        ), f"envoy image upgrade rolled no envoy pod (rolls_envoy={rolls_envoy}); upgrade was a no-op"
        _assert_rolled_through(
            paging.verdict,
            ok_at_recovery,
            ok_after,
            "envoy-image",
            disruption_s=disruption_s,
            bound_s=DISRUPTION_BOUND_S,
        )
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)


def _set_operator_env(namespace: str, deployment_name: str, env_name: str, value: str) -> None:
    """Set an env var on the operator container and let the Deployment roll the operator pod."""
    apps = client.AppsV1Api()
    dep = apps.read_namespaced_deployment(deployment_name, namespace)
    container = dep.spec.template.spec.containers[0]
    env = container.env or []
    for e in env:
        if e.name == env_name:
            e.value = value
            break
    else:
        env.append(client.V1EnvVar(name=env_name, value=value))
    container.env = env
    apps.patch_namespaced_deployment(deployment_name, namespace, dep)
    logger.info(f"set operator {env_name}={value} on {deployment_name}")
