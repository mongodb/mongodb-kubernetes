"""Search availability across off->on TLS enablement, then a cert rotation.

Bootstraps a NON-TLS search deployment (source mongod ``searchTLSMode=disabled``, MongoDBSearch CR
without ``spec.security.tls`` -> mongot serves plaintext gRPC, envoy plaintext), then enables search
TLS under sustained query load: issue the LB + search certs, set ``spec.security.tls.certsSecretPrefix``
and flip the source mongod to ``searchTLSMode=requireTLS``. Both mongot and envoy roll onto TLS.

Enabling search TLS is INHERENTLY a bounded outage, not a ride-through: mongot's gRPC TLS mode is
binary (plaintext or TLS, no dual-listen) and no mongod ``searchTLSMode`` tolerates both endpoint
kinds at once, so the plaintext->TLS data-plane cutover drops in-flight $search until mongod and
mongot reconverge (the source-mongod automation agent applying requireTLS dominates the tail). The
suite therefore asserts the deployment ROLLS both groups onto TLS, the mongod reaches requireTLS, and
search RECOVERS to steady state (the recovery wait bounds it) — and logs the observed disruption — it
does not assert no-outage. A second scenario then rotates the now-live cert (a ride-through; both
groups roll onto the new material). Resource/version changes live in the upgrade suites.
"""

from __future__ import annotations

import time

import pytest
from kubetester.mongodb import MongoDB
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

pytestmark = pytest.mark.e2e_search_availability_tls_enable

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-avail-tlsen")
# Bootstrap NON-TLS: mongot plaintext gRPC, no CR spec.security.tls, mongod searchTLSMode=disabled.
SEARCH = SearchDeploymentConfig(search_tls=False)
MDBS_NAME = MDB.mdb_resource_name
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"
ENVOY_DEPLOYMENT = search_resource_names.lb_deployment_name(MDBS_NAME)
ENVOY_SELECTOR = f"app={ENVOY_DEPLOYMENT}"

# Cert prefix the bootstrap would have used (SearchDeploymentConfig.tls_cert_prefix); issued at
# enable time. _ROTATED_PREFIX is the second prefix the rotation scenario moves onto.
_TLS_PREFIX = SEARCH.tls_cert_prefix
_ROTATED_PREFIX = "certs-rot"

BASELINE_OPS = 30
POST_EVENT_OPS = 15

# Recovery allowance for the enable cutover: the source-mongod agent reconverging searchTLSMode
# dominates and runs well past pod-Ready, so the recovery wait is generous. Doubles as the outage
# bound — if search does not recover within it, the steady-state gate fails the test.
ENABLE_RECOVERY_TIMEOUT = 420
# Rotation is a ride-through; the surviving replica serves through a one-at-a-time roll.
ROTATION_DISRUPTION_BOUND_S = 60.0


# --- shared helpers -------------------------------------------------------


def _emit_metric(path: str, *, rolls_mongot: int, rolls_envoy: int, recovery_s: float, disruption_s: float) -> None:
    """One greppable SEARCH_TLS_METRIC line per path; the roll/disruption stories cite it from the log."""
    logger.info(
        f"SEARCH_TLS_METRIC path={path} rolls_mongot={rolls_mongot} rolls_envoy={rolls_envoy} "
        f"recovery_s={recovery_s:.1f} disruption_s={disruption_s:.1f}"
    )


def _user_tool(namespace: str) -> SearchConnectivityTool:
    """A fresh tool (own pymongo client) per call — never share one across concurrent testers.

    The client talks to mongod over the mongod's own (always-on) TLS; only the mongod<->mongot search
    channel is being toggled, so the client connection is identical before and after enablement.
    """
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
        set_search_tls=False,
    )


def _set_mongod_search_tls_mode(namespace: str, mode: str) -> None:
    """Flip the external source mongod's searchTLSMode (the operator does not own an external source)."""
    mdb = MongoDB(name=MDB.mdb_resource_name, namespace=namespace)
    mdb.load()
    mdb["spec"]["additionalMongodConfig"]["setParameter"]["searchTLSMode"] = mode
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


def _assert_steady(namespace: str, *, sentinel_timeout: int = 300) -> None:
    """Recovery/steady-state gate: a clean window on both query types (fresh client per tester).

    wait_for_sentinel_indexed blocks until $search serves again, so this doubles as the recovery
    wait after the enable cutover — its timeout bounds how long the outage may last."""
    _user_tool(namespace).wait_for_sentinel_indexed(timeout=sentinel_timeout)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(_user_tool(namespace), mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


def _mongod_search_tls_mode(namespace: str) -> str:
    mdb = MongoDB(name=MDB.mdb_resource_name, namespace=namespace)
    mdb.load()
    return mdb["spec"]["additionalMongodConfig"]["setParameter"]["searchTLSMode"]


# --- deploy chain (once per file, NON-TLS) --------------------------------


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


# --- scenario: off->on search TLS enablement (bounded outage) --------------


class TestEnableSearchTLS:
    def test_steady_state_before(self, namespace: str):
        """Non-TLS search serves both query types before enablement."""
        _assert_steady(namespace)

    def test_issue_certs(self, namespace: str):
        """Issue the LB + search server certs the CR will point at when TLS is enabled.
        The index-0 headless-svc SAN matches the RS mongot StatefulSet the operator deploys."""
        issuer = ensure_search_issuer(namespace)
        create_rs_lb_certificates(namespace, issuer, MDBS_NAME, _TLS_PREFIX)
        indexed_svc = search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)
        create_rs_search_tls_cert(
            namespace,
            issuer,
            MDBS_NAME,
            _TLS_PREFIX,
            extra_domains=[f"{indexed_svc}.{namespace}.svc.cluster.local"],
        )

    def test_enable_tls_availability(self, namespace: str):
        """Enable search TLS under load: add spec.security.tls to the CR and flip the source mongod to
        requireTLS. Both mongot (StatefulSet) and envoy (managed-LB Deployment) roll onto TLS. The
        plaintext->TLS cutover is a bounded outage that drops in-flight $search until mongod and mongot
        reconverge. The background testers OBSERVE the outage; recovery is proven by a fresh-client
        steady-state window (the stale in-flight clients do not cleanly resume across the cutover, so
        recovery is asserted with fresh clients, not by the same testers). Assert both groups roll, the
        mongod reaches requireTLS, and search recovers; log the observed disruption."""
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            mongot_before = _pod_uids(namespace, MONGOT_SELECTOR)
            envoy_before = _pod_uids(namespace, ENVOY_SELECTOR)
            t0 = time.monotonic()
            mdbs = _load_mdbs(namespace)
            mdbs.load()
            mdbs["spec"]["security"] = {"tls": {"certsSecretPrefix": _TLS_PREFIX}}
            mdbs.update()
            _set_mongod_search_tls_mode(namespace, "requireTLS")
            wait_for_all_pods_replaced(namespace, mongot_before)
            wait_for_pods_by_label_replaced(namespace, ENVOY_SELECTOR, envoy_before, expected=SEARCH.envoy_lb_replicas)
            mdbs.assert_reaches_phase(Phase.Running, timeout=600)
            # Fresh-client $search proves the data plane serves TLS again; bounds the outage.
            _user_tool(namespace).wait_for_sentinel_indexed(timeout=ENABLE_RECOVERY_TIMEOUT)
            recovery_s = time.monotonic() - t0
        disruption_s = paging.max_consecutive_failure * paging.interval_seconds
        rolls_mongot = _roll_count(namespace, MONGOT_SELECTOR, mongot_before)
        rolls_envoy = _roll_count(namespace, ENVOY_SELECTOR, envoy_before)
        logger.info(f"tls-enable oneshot verdict: {oneshot.verdict.as_dict()}")
        logger.info(f"tls-enable paging  verdict: {paging.verdict.as_dict()}")
        _emit_metric(
            "tls-enable",
            rolls_mongot=rolls_mongot,
            rolls_envoy=rolls_envoy,
            recovery_s=recovery_s,
            disruption_s=disruption_s,
        )
        assert (
            rolls_mongot >= SEARCH.mongot_replicas
        ), f"expected all {SEARCH.mongot_replicas} mongot to roll onto TLS, got {rolls_mongot}"
        assert rolls_envoy >= 1, f"expected envoy to roll onto TLS, but rolls_envoy={rolls_envoy}"
        assert (
            _mongod_search_tls_mode(namespace) == "requireTLS"
        ), "source mongod did not reach searchTLSMode=requireTLS after enablement"
        # Recovery is the contract: fresh clients prove a fully clean window post-cutover.
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)


# --- scenario: search-server TLS cert rotation (ride-through) --------------


class TestRotateSearchTLSCert:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_issue_rotated_certs(self, namespace: str):
        """Issue a fresh LB + search cert under the rotated prefix, mirroring the enable cert step."""
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
        """Point certsSecretPrefix at the rotated cert: both mongot and envoy carry the server cert,
        so both roll onto the new material while a surviving replica of each serves new queries and the
        open cursor serves fresh pages after recovery."""
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
            ok_at_recovery = paging.succeeded_count
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
            bound_s=ROTATION_DISRUPTION_BOUND_S,
        )
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)
