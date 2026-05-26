"""E2E verification of pull-based CR replication in a Raft-based HA multi-cluster
operator deployment.

Scenario:
    1. 3-cluster HA environment.  The bootstrap cluster becomes the initial Raft
       leader.
    2. A ``MongoDBMultiCluster`` CR (``pull-replication-test``) is created on the
       leader cluster's Kubernetes API.
    3. Each follower's local Kubernetes API is polled until it exposes the
       same-named CR with ``haraft.mongodb.com/replica-source`` pointing at the
       leader cluster name — confirming pull-based replication worked.
    4. The CR is patched on the leader (member count for one cluster entry is
       changed).  Each follower's local replica is polled until it reflects the
       updated spec.
    5. Follower replicas have an empty / unset ``status.phase`` — the leader is
       the sole status writer.

Note: This test requires a 3-cluster kind environment with the HA POC bits
(Raft, kubectl-mongodb leader CLI, CRPuller) wired up.  It will not pass in
environments that lack that setup — it is collection-clean and is intended for
the dedicated HA-enabled e2e variant.

TODO (implementer): Register this test module in the e2e runner configuration.
    * Find the existing ``multi_cluster_ha_failover`` entry in the pytest
      collection (typically ``scripts/dev/contexts/e2e_multi_cluster_ha`` or the
      Evergreen / Make test-list).
    * Add a parallel entry for ``multi_cluster_ha_pull_replication``.
    * The new pytest mark is ``e2e_multi_cluster_ha_pull_replication``; add it to
      the pytest.ini / pyproject.toml ``markers`` section to avoid warnings.
"""

import subprocess
import time

import kubernetes
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from pytest import fixture, mark

from tests.conftest import get_member_cluster_api_client

from .conftest import cluster_spec_list

# ---------------------------------------------------------------------------
# Annotation / ConfigMap constants (mirrors pkg/haraft/cr_puller.go and
# pkg/haraft/types.go so we stay in sync with the Go code).
# ---------------------------------------------------------------------------

REPLICA_SOURCE_ANNOTATION = "haraft.mongodb.com/replica-source"

# Name of the MongoDBMultiCluster CR under test.  A short, unique name avoids
# collisions with the "multi-replica-set" CR used by other HA tests in the
# same suite.
CR_NAME = "pull-replication-test"

# How long (seconds) to wait for CRPuller to mirror a CR to all followers.
PROPAGATION_TIMEOUT_S = 60

# Shared mutable state (initial leader cluster name) set by test_capture_initial_leader
# and consumed by subsequent tests.
_state: dict = {}

# CRD group/version/plural for MongoDBMultiCluster.
_CR_GROUP = "mongodb.com"
_CR_VERSION = "v1"
_CR_PLURAL = "mongodbmulticluster"


# ---------------------------------------------------------------------------
# Helpers (mirror the pattern used in multi_cluster_ha_failover.py /
# multi_cluster_ha_quorum_loss.py — no new abstraction module).
# ---------------------------------------------------------------------------


def _kubectl_mongodb_leader(namespace: str) -> str:
    """Return the current Raft leader cluster name via the CLI plugin."""
    return subprocess.check_output(
        ["./bin/kubectl-mongodb", "multicluster", "leader", "--namespace", namespace],
        text=True,
    ).strip()


def _get_cr_from_cluster(cluster_name: str, cr_name: str, namespace: str) -> dict | None:
    """Fetch a MongoDBMultiCluster custom object from a specific member cluster.

    Returns the raw dict from the Kubernetes API, or ``None`` if not found.
    Raises for all other API errors so callers see genuine failures.
    """
    api_client = get_member_cluster_api_client(cluster_name)
    custom_api = kubernetes.client.CustomObjectsApi(api_client)
    try:
        return custom_api.get_namespaced_custom_object(
            group=_CR_GROUP,
            version=_CR_VERSION,
            namespace=namespace,
            plural=_CR_PLURAL,
            name=cr_name,
        )
    except kubernetes.client.exceptions.ApiException as exc:
        if exc.status == 404:
            return None
        raise


def _poll_until(condition, timeout_s: float, interval_s: float = 2.0, description: str = "condition"):
    """Busy-poll ``condition()`` until it returns a truthy value or we time out.

    Returns the truthy value from ``condition()``.
    Raises ``AssertionError`` on timeout.
    """
    deadline = time.monotonic() + timeout_s
    last_exc: Exception | None = None
    while time.monotonic() < deadline:
        try:
            result = condition()
            if result:
                return result
        except Exception as exc:  # noqa: BLE001
            last_exc = exc
        time.sleep(interval_s)
    raise AssertionError(
        f"timed out after {timeout_s}s waiting for {description} "
        f"(last error: {last_exc!r})"
    )


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    """A MongoDBMulti resource fixture pointing at the central (leader) cluster.

    The resource is NOT applied here — each test that needs it calls
    ``mongodb_multi.update()`` explicitly, matching the failover-test pattern.
    """
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), CR_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [1, 1, 1])

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


# ---------------------------------------------------------------------------
# Tests (ordered — each builds on the previous via _state and the shared CR)
# ---------------------------------------------------------------------------


@mark.e2e_multi_cluster_ha_pull_replication
def test_deploy_operator(multi_cluster_operator: Operator):
    """Sanity-check that the operator is up before exercising pull replication."""
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_ha_pull_replication
def test_capture_initial_leader(namespace: str, member_cluster_names: list[str]):
    """Record the Raft leader cluster name; derive follower list for later tests."""
    leader = _kubectl_mongodb_leader(namespace)
    assert leader, "kubectl-mongodb returned an empty leader name"

    followers = [c for c in member_cluster_names if c != leader]
    assert followers, (
        f"no followers found (leader={leader!r}, members={member_cluster_names!r})"
    )

    _state["leader"] = leader
    _state["followers"] = followers
    print(f"Initial Raft leader: {leader}; followers: {followers}")


@mark.e2e_multi_cluster_ha_pull_replication
def test_create_cr_on_leader(mongodb_multi: MongoDBMulti):
    """Create the MongoDBMultiCluster CR on the leader cluster's API server."""
    mongodb_multi.update()
    # We intentionally do NOT wait for Phase.Running here: this test only
    # verifies replication, not full MongoDB convergence.  Waiting for
    # Running would add >10 minutes to the test on small kind clusters.
    print(f"CR {CR_NAME!r} applied to leader cluster.")


@mark.e2e_multi_cluster_ha_pull_replication
def test_initial_replica_appears_on_followers(namespace: str):
    """Each follower must expose a local replica of the CR with the
    ``haraft.mongodb.com/replica-source`` annotation pointing at the leader.

    The CRPuller runs on a 2-second resync interval; we allow up to
    ``PROPAGATION_TIMEOUT_S`` seconds for propagation.
    """
    leader = _state["leader"]
    followers = _state["followers"]

    for follower in followers:
        def _replica_present(f=follower):
            return _get_cr_from_cluster(f, CR_NAME, namespace)

        obj = _poll_until(
            _replica_present,
            timeout_s=PROPAGATION_TIMEOUT_S,
            description=f"CR {CR_NAME!r} to appear on follower {follower!r}",
        )

        annotations = (obj.get("metadata") or {}).get("annotations") or {}
        source = annotations.get(REPLICA_SOURCE_ANNOTATION)
        assert source == leader, (
            f"follower {follower!r}: expected replica-source annotation "
            f"{leader!r}, got {source!r}"
        )
        print(f"Follower {follower}: replica present with replica-source={source!r}")


@mark.e2e_multi_cluster_ha_pull_replication
def test_spec_edit_propagates_to_followers(namespace: str, member_cluster_names: list[str]):
    """Patch the CR on the leader (change member count for the first cluster
    entry) and verify each follower's local replica reflects the new spec.

    We use the leader's API client directly (via get_member_cluster_api_client)
    so the patch bypasses any central-cluster routing and hits the leader's
    Kubernetes API server.
    """
    leader = _state["leader"]
    followers = _state["followers"]

    leader_client = get_member_cluster_api_client(leader)
    custom_api = kubernetes.client.CustomObjectsApi(leader_client)

    # Read the current spec so we know what to change.
    current = custom_api.get_namespaced_custom_object(
        group=_CR_GROUP,
        version=_CR_VERSION,
        namespace=namespace,
        plural=_CR_PLURAL,
        name=CR_NAME,
    )
    cluster_spec_list_current = current.get("spec", {}).get("clusterSpecList", [])
    assert cluster_spec_list_current, "clusterSpecList is empty — cannot patch"

    # Flip member count between 1 and 2 for the first cluster entry.
    old_count = cluster_spec_list_current[0].get("members", 1)
    new_count = 2 if old_count == 1 else 1
    cluster_spec_list_current[0]["members"] = new_count

    patch_body = {"spec": {"clusterSpecList": cluster_spec_list_current}}
    custom_api.patch_namespaced_custom_object(
        group=_CR_GROUP,
        version=_CR_VERSION,
        namespace=namespace,
        plural=_CR_PLURAL,
        name=CR_NAME,
        body=patch_body,
    )
    print(
        f"Patched clusterSpecList[0].members: {old_count} -> {new_count} on leader {leader!r}"
    )

    # Each follower should see the updated member count within the propagation window.
    for follower in followers:
        def _updated(f=follower, expected=new_count):
            obj = _get_cr_from_cluster(f, CR_NAME, namespace)
            if obj is None:
                return None
            entries = obj.get("spec", {}).get("clusterSpecList", [])
            if not entries:
                return None
            return entries[0].get("members") == expected or None

        _poll_until(
            _updated,
            timeout_s=PROPAGATION_TIMEOUT_S,
            description=(
                f"clusterSpecList[0].members={new_count} to propagate "
                f"to follower {follower!r}"
            ),
        )
        print(f"Follower {follower}: propagated clusterSpecList[0].members={new_count}")


@mark.e2e_multi_cluster_ha_pull_replication
def test_followers_do_not_write_status(namespace: str):
    """Follower replicas must have an empty or unset ``status.phase``.

    The CRPuller copies only spec — the leader is the sole status writer.
    We give the leader's reconciler a brief settle window, then check every
    follower's local replica.
    """
    followers = _state["followers"]

    # 10-second settle matches the design doc's guidance; keeps test fast on
    # clusters where reconciliation is quick.
    time.sleep(10)

    for follower in followers:
        obj = _get_cr_from_cluster(follower, CR_NAME, namespace)
        assert obj is not None, (
            f"follower {follower!r}: replica {CR_NAME!r} disappeared unexpectedly"
        )
        status = obj.get("status") or {}
        phase = status.get("phase", "")
        assert not phase, (
            f"follower {follower!r} unexpectedly wrote status.phase={phase!r} "
            "on its local replica; the leader should be the sole status writer"
        )
        print(f"Follower {follower}: status.phase is empty/unset (correct).")
