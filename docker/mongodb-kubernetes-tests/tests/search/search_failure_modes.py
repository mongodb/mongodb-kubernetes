"""E2E failure-mode scenarios for the background availability tester (KUBE-27).

Layered on the background tester from KUBE-26 (which is layered on the
connectivity tool from KUBE-17), this file enumerates discrete failure
modes from the GA availability matrix and proves each one surfaces
through the harness as a real verdict failure — not a silent pass.

Why this exists at all: every downstream availability test ("upgrade
preserves cursors", "GOAWAY drains cleanly", etc.) takes the form
"start the tester, do the thing, assert the verdict". If the tester
silently passes through a fault — because mongod's cache served the
cursor, or envoy retried, or the new pod came up too fast — every
downstream test becomes a no-op. KUBE-27 nails down which faults the
harness can actually catch and how.

Scenarios in this file:

1. ``test_failure_mongot_pod_restart_surfaces_outage`` — kill EVERY
   mongot pod, StatefulSet recreates them. Tests that pod-level
   mongot restarts surface as ``transient_network`` (envoy can't
   reach upstream while pods are scheduling) or ``cursor_lost``
   (HTTP/2 stream torn down). Multi-replica fixture means deleting
   only one pod leaves the other replica serving and the harness
   sees no fault — so we delete the entire StatefulSet's pod set.

Out of scope for the first cut (TBD follow-ups, each needs more
infrastructure than this single-cluster RS fixture provides):

2. Envoy single-pod restart — prototyped but deferred. With the
   default ``terminationGracePeriodSeconds=30`` the OLD envoy pod
   stays in Terminating but continues to accept traffic via the live
   HTTP/2 connection from mongod through the service endpoint, while
   the new pod is concurrently coming up — so mongod sees no gap.
   Catching the failure mode would require either ``--grace-period=0``
   force-delete (loses the graceful-drain semantics that are actually
   a GA requirement) OR taking down the entire managed-LB Deployment
   via ``replicas=0`` (different fault from a single-pod restart).
   Pairs with the GOAWAY drain test (KUBE-45 envoy lifecycle) which
   exercises the same path with proper disruption-bound assertions.
3. Query before the search index has finished building — needs the
   index to be deleted+recreated mid-test, with a probe interleaved
   while it's still building. Tracks against the search-index
   lifecycle test family (KUBE-39 / KUBE-30).
4. One mongod instance missing search parameters — needs ad-hoc
   mongod setParameter manipulation; probably an automation_config
   hack against the OM project.
5. New shard added mid-flight — needs a sharded fixture, not the
   single-replica-set this PR's bootstrap deploys; tracks against
   KUBE-39 topology evolution tests.

The deployment scaffolding mirrors the KUBE-26 background-tester e2e
and is delegated to ``tests.common.search.connectivity_bootstrap``.
"""

from __future__ import annotations

import time

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
from tests.common.search.availability_tester import SearchAvailabilityBackgroundTester
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.rs_search_helper import get_rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.sharded_search_helper import create_issuer_ca

logger = test_logger.get_test_logger(__name__)

# Fixture identity. Distinct from the KUBE-17 / KUBE-26 fixtures so this
# file's resources don't collide with sibling tests when run in parallel
# on a warm cluster.
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"
ENVOY_PROXY_PORT = 27028

MDB_RESOURCE_NAME = "mdb-rs-fail-modes"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
RS_MEMBERS = 3
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

# Fault windows. Each scenario opens a short healthy probe first so the
# verdict has known-good baseline iterations, then introduces the fault
# and continues probing for long enough to capture the recovery window.
HEALTHY_BASELINE_SECONDS = 6.0
FAULT_OBSERVATION_SECONDS = 25.0


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


# Cluster bootstrap — identical sequence to KUBE-26 / KUBE-17 (delegated
# to tests.common.search.connectivity_bootstrap).
@mark.e2e_search_failure_modes
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    bootstrap.install_operator(namespace, operator_installation_config)


@mark.e2e_search_failure_modes
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    bootstrap.create_ops_manager(namespace)


@mark.e2e_search_failure_modes
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    bootstrap.install_tls_certificates(helper, issuer, RS_MEMBERS)


@mark.e2e_search_failure_modes
def test_create_database_resource(mdb: MongoDB):
    bootstrap.create_database_resource(mdb)


@mark.e2e_search_failure_modes
def test_create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
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


@mark.e2e_search_failure_modes
def test_deploy_lb_certificates(namespace: str, issuer: str):
    bootstrap.deploy_lb_certificates(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_failure_modes
def test_create_search_tls_certificate(namespace: str, issuer: str):
    bootstrap.create_search_tls_certificate(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_failure_modes
def test_create_search_resource(mdbs: MongoDBSearch):
    bootstrap.create_search_resource(mdbs)


@mark.e2e_search_failure_modes
def test_verify_envoy_deployment(namespace: str):
    bootstrap.verify_envoy_deployment(namespace, MDBS_RESOURCE_NAME)


@mark.e2e_search_failure_modes
def test_wait_for_database_ready(mdb: MongoDB):
    bootstrap.wait_for_database_ready(mdb)


@mark.e2e_search_failure_modes
def test_verify_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    bootstrap.verify_mongod_parameters(namespace, MDB_RESOURCE_NAME, RS_MEMBERS, mdbs.name, ENVOY_PROXY_PORT)


@mark.e2e_search_failure_modes
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_failure_modes
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    bootstrap.restore_sample_database(mdb, tools_pod, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)


@mark.e2e_search_failure_modes
def test_create_search_index(mdb: MongoDB):
    bootstrap.create_search_index(mdb, USER_NAME, USER_PASSWORD)


# ----------------------------------------------------------------------
# Failure-mode scenarios — KUBE-27 deliverable
# ----------------------------------------------------------------------


def _new_tester(mdb: MongoDB) -> SearchAvailabilityBackgroundTester:
    """Build a fresh oneshot-mode tester wired against the same fixture.

    All KUBE-27 scenarios use oneshot mode for the same reason as
    KUBE-26's outage scenario: a long-living paging cursor's getMore
    can be served from mongod's cache during the fault window, hiding
    the upstream loss. Oneshot with cache-busting forces a real
    upstream evaluation each iteration, so the fault surfaces every
    time the harness ticks.
    """
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    tool = SearchConnectivityTool(search_tester, cache_latency_threshold_ms=10.0)
    return SearchAvailabilityBackgroundTester(
        tool,
        mode="oneshot",
        wait_sec=0.5,
    )


def _wait_for_pod_recreation(
    namespace: str,
    label_selector: str,
    excluded_uids: set[str],
    timeout: int = 180,
) -> None:
    """Block until at least one pod matching label_selector is Ready
    AND its uid is NOT in excluded_uids.

    Used by every fault scenario's cleanup so the next scenario
    starts on a healthy cluster.
    """
    core_v1 = client.CoreV1Api()

    def _ready_with_new_uid() -> tuple[bool, str]:
        pods = core_v1.list_namespaced_pod(namespace=namespace, label_selector=label_selector).items
        if not pods:
            return False, "no pods match selector"
        fresh = [
            p
            for p in pods
            if p.metadata.uid not in excluded_uids
            and any(c.type == "Ready" and c.status == "True" for c in (p.status.conditions or []))
        ]
        return len(fresh) > 0, f"matching={len(pods)} fresh-and-ready={len(fresh)}"

    run_periodically(
        _ready_with_new_uid,
        timeout=timeout,
        sleep_time=5,
        msg=f"pod recreation for selector '{label_selector}'",
    )


def _delete_pods_in_label(namespace: str, label_selector: str) -> set[str]:
    """Delete every pod matching label_selector, return their old uids."""
    core_v1 = client.CoreV1Api()
    pods = core_v1.list_namespaced_pod(namespace=namespace, label_selector=label_selector).items
    if not pods:
        # Some resources don't have the canonical pod-name label; the
        # caller is responsible for providing a working selector.
        return set()
    uids = {p.metadata.uid for p in pods}
    for p in pods:
        logger.info(f"deleting pod {p.metadata.name} (uid={p.metadata.uid})")
        core_v1.delete_namespaced_pod(name=p.metadata.name, namespace=namespace)
    return uids


@mark.e2e_search_failure_modes
def test_failure_mongot_pod_restart_surfaces_outage(mdb: MongoDB, mdbs: MongoDBSearch, namespace: str):
    """Scenario 1 — mongot pod restart.

    Delete EVERY mongot pod. The search-rs-managed-lb fixture has
    ``spec.replicas: 2``; deleting only one pod would leave the
    other replica serving and the tester would see no fault. The
    StatefulSet controller recreates each pod within ~10-30s; during
    the recreation window envoy has no healthy upstream, so the
    tester's oneshot probes must surface ``transient_network``
    failures (or ``cursor_lost`` if the timing catches a cursor
    mid-getMore).
    """
    statefulset_name = search_resource_names.mongot_statefulset_name(mdbs.name)
    # StatefulSet's pod template has ``app=<sts-name>-svc`` (matches
    # the headless service selector); using this label catches every
    # replica rather than just pod-0.
    pod_label = f"app={statefulset_name}-svc"

    tester = _new_tester(mdb)
    tester.start()
    try:
        time.sleep(HEALTHY_BASELINE_SECONDS)
        excluded = _delete_pods_in_label(namespace, pod_label)
        assert excluded, f"no mongot pods matched selector '{pod_label}'"
        time.sleep(FAULT_OBSERVATION_SECONDS)
    finally:
        tester.stop()
        tester.join(timeout=10)
        assert not tester.is_alive(), "background tester thread did not exit cleanly"
        try:
            _wait_for_pod_recreation(namespace, pod_label, excluded)
        except Exception as e:
            logger.warning(f"mongot pod recreation wait timed out in cleanup: {e}")

    verdict = tester.assert_outage_detected(accept_classes=("cursor_lost", "transient_network"))
    logger.info(f"mongot-restart verdict: {verdict.as_dict()}")
    assert verdict.upstream_alive, (
        f"verdict has no upstream-confirmed iterations at all — the harness never reached "
        f"upstream, so 'failure detected' is meaningless. verdict={verdict.as_dict()}"
    )


# NOTE: a second scenario (envoy single-pod restart) was prototyped
# but deferred from this PR. Empirical finding: deleting the envoy pod
# does NOT cause query failures the harness can catch in this fixture's
# managed-LB configuration. With the default
# ``terminationGracePeriodSeconds=30`` the OLD envoy pod stays in
# Terminating but continues to accept traffic via the live HTTP/2
# connection from mongod through the service endpoint, while the new
# pod is concurrently coming up — so mongod sees no gap. Demonstrating
# the failure mode would require either ``--grace-period=0`` force-
# delete (loses the graceful-drain semantics that are actually a GA
# requirement) OR taking down the entire managed-LB Deployment via
# ``replicas=0`` (different fault from a single-pod restart).
# Tracked as a follow-up; pairs with the GOAWAY drain test
# (KUBE-45 envoy lifecycle) which exercises the SAME path with proper
# disruption-bound assertions.
