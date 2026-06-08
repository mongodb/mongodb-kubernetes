"""Search availability across a search-server TLS cert rotation.

Rotates the MongoDBSearch server cert under sustained query load: the deployment starts with the
harness-default TLS (``certsSecretPrefix=certs``), a fresh LB + search cert is issued under a new
prefix, and the CR's ``spec.security.tls.certsSecretPrefix`` is pointed at it. Both mongot and envoy
carry the server cert, so both roll onto the new material; the background tester window asserts the
open cursor serves fresh pages after recovery. The mongod source stays ``searchTLSMode: requireTLS``
throughout, so this is a rotation, not an off->on enable (that needs a non-TLS bootstrap variant).
Resource-spec changes live in search_availability_config_change.py.
"""

from __future__ import annotations

import time

import pytest
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
from tests.common.search.rs_search_helper import create_rs_lb_certificates, create_rs_search_tls_cert, rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper, ensure_search_issuer
from tests.common.search.upgrade_availability import assert_rolled_through as _assert_rolled_through
from tests.common.search.upgrade_availability import pod_uids as _pod_uids
from tests.common.search.upgrade_availability import roll_count as _roll_count
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_availability_tls_rotation

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-avail-tls")
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

# New cert-secret prefix to rotate onto (the bootstrap deploys with SearchDeploymentConfig.tls_cert_prefix).
_ROTATED_PREFIX = "certs-rot"


# --- shared helpers -------------------------------------------------------


def _emit_metric(path: str, *, rolls_mongot: int, rolls_envoy: int, recovery_s: float, disruption_s: float) -> None:
    """One greppable SEARCH_CONFIG_METRIC line per path; the roll/disruption stories cite it from the log."""
    logger.info(
        f"SEARCH_CONFIG_METRIC path={path} rolls_mongot={rolls_mongot} rolls_envoy={rolls_envoy} "
        f"recovery_s={recovery_s:.1f} disruption_s={disruption_s:.1f}"
    )


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
    """Recovery/steady-state gate: a clean window on both query types (fresh client per tester)."""
    _user_tool(namespace).wait_for_sentinel_indexed(timeout=300)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(_user_tool(namespace), mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


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


# --- scenario: search-server TLS cert rotation (CR spec.security.tls.certsSecretPrefix) ---


class TestRotateSearchTLSCert:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_issue_rotated_certs(self, namespace: str):
        """Issue a fresh LB + search cert under the new prefix, mirroring the bootstrap's TLS step.
        The index-0 headless-svc SAN matches the RS mongot StatefulSet the operator deploys."""
        issuer = ensure_search_issuer(namespace)
        create_rs_lb_certificates(namespace, issuer, MDBS_NAME, _ROTATED_PREFIX)
        indexed_svc = search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)
        create_rs_search_tls_cert(
            namespace,
            issuer,
            MDBS_NAME,
            _ROTATED_PREFIX,
            extra_domains=[f"{indexed_svc}.{namespace}.svc.cluster.local"],
        )

    def test_tls_rotation_availability(self, namespace: str):
        """Point certsSecretPrefix at the rotated cert: both mongot (StatefulSet) and envoy (managed-LB
        Deployment) carry the server cert, so both roll onto the new material while the surviving
        replica of each serves new queries and the open cursor serves fresh pages after recovery."""
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            mongot_before = _pod_uids(namespace, MONGOT_SELECTOR)
            envoy_before = _pod_uids(namespace, ENVOY_SELECTOR)
            mdbs = _load_mdbs(namespace)
            mdbs.load()
            mdbs["spec"]["security"]["tls"]["certsSecretPrefix"] = _ROTATED_PREFIX
            t0 = time.monotonic()
            mdbs.update()
            wait_for_all_pods_replaced(namespace, mongot_before)
            wait_for_pods_by_label_replaced(namespace, ENVOY_SELECTOR, envoy_before, expected=SEARCH.envoy_lb_replicas)
            mdbs.assert_reaches_phase(Phase.Running, timeout=600)
            recovery_s = time.monotonic() - t0
            ok_at_recovery = paging.succeeded_count  # snapshot once pods are Ready again
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
            ok_after = paging.succeeded_count
        disruption_s = paging.max_consecutive_failure * paging.interval_seconds
        rolls_mongot = _roll_count(namespace, MONGOT_SELECTOR, mongot_before)
        rolls_envoy = _roll_count(namespace, ENVOY_SELECTOR, envoy_before)
        logger.info(f"tls-rotation oneshot verdict: {oneshot.verdict.as_dict()}")
        _emit_metric(
            "tls-rotation",
            rolls_mongot=rolls_mongot,
            rolls_envoy=rolls_envoy,
            recovery_s=recovery_s,
            disruption_s=disruption_s,
        )
        assert (
            rolls_mongot >= SEARCH.mongot_replicas
        ), f"expected all {SEARCH.mongot_replicas} mongot to roll onto the new cert, got {rolls_mongot}"
        assert rolls_envoy >= 1, f"expected envoy to roll onto the new cert, but rolls_envoy={rolls_envoy}"
        _assert_rolled_through(
            paging.verdict,
            ok_at_recovery,
            ok_after,
            "tls-rotation",
            disruption_s=disruption_s,
            bound_s=DISRUPTION_BOUND_S,
        )
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)
