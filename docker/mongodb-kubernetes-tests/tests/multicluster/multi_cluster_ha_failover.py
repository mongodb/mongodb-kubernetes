"""E2E test for Raft-based HA failover across operator instances in a multi-cluster
deployment.

Scenario:
    1. Operator runs on every member cluster with Raft initialized. A
       ``MongoDBMultiCluster`` CR is applied via the current leader cluster's
       Kubernetes API and is expected to reach ``Running``.
    2. The leader cluster's Kubernetes API server is killed (in kind we
       ``docker stop`` the kind nodes for that cluster).
    3. Within ~30s a new leader should be elected. We verify this via the
       ``kubectl-mongodb multicluster leader`` CLI, which must return a
       different cluster name than the original leader.
    4. A spec change is applied to the CR via the new leader cluster and the CR
       is expected to reconcile back to ``Running``.

Note: This test requires a 3-cluster kind environment with the HA POC bits
(Raft, kubectl-mongodb leader CLI, etc.) wired up. It will not pass in
environments lacking that setup -- it is collection-clean and intended to be
exercised on a dedicated HA-enabled e2e variant.
"""

import subprocess
import time

import kubernetes
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark

from tests.conftest import get_member_cluster_api_client

from .conftest import cluster_spec_list

# Annotation set by CRPuller on every locally mirrored MongoDBMultiCluster replica.
# Must stay in sync with pkg/haraft/cr_puller.go:ReplicaSourceAnnotation.
_REPLICA_SOURCE_ANNOTATION = "haraft.mongodb.com/replica-source"

# CRD identifiers used when querying member-cluster API servers directly.
_CR_GROUP = "mongodb.com"
_CR_VERSION = "v1"
_CR_PLURAL = "mongodbmulticluster"

# Shared mutable state across the ordered test functions. Module-scope
# fixtures cache the initial leader and the new leader after failover so
# subsequent tests can target the correct cluster's API server.
_state: dict = {}


def _kubectl_mongodb_leader(namespace: str) -> str:
    """Return the current Raft leader cluster name by invoking the
    ``kubectl-mongodb multicluster leader`` plugin.

    The binary lives at ``./bin/kubectl-mongodb`` relative to the repo root
    during e2e runs. In environments where it's not built, this will raise --
    that's fine for collection.
    """
    return subprocess.check_output(
        ["./bin/kubectl-mongodb", "multicluster", "leader", "--namespace", namespace],
        text=True,
    ).strip()


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource


@mark.e2e_multi_cluster_ha_failover
def test_deploy_operator(multi_cluster_operator: Operator):
    """Sanity-check that the operator is up before exercising failover."""
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_ha_failover
def test_capture_initial_leader(namespace: str):
    """Record the cluster currently acting as the Raft leader."""
    initial_leader = _kubectl_mongodb_leader(namespace)
    assert initial_leader, "kubectl-mongodb returned an empty leader name"
    _state["initial_leader"] = initial_leader
    print(f"Initial Raft leader: {initial_leader}")


@mark.e2e_multi_cluster_ha_failover
def test_apply_cr_to_initial_leader(mongodb_multi: MongoDBMulti):
    """Apply the MongoDBMultiCluster CR via the initial leader cluster and
    wait for it to reach ``Running``.

    The ``central_cluster_client`` used by the ``mongodb_multi`` fixture is
    expected to be pointed at the current Raft leader in the HA test
    environment, so a plain ``update()`` exercises the leader's API path.
    """
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_multi_cluster_ha_failover
def test_kill_leader_cluster():
    """Stop the kind containers backing the current leader cluster.

    This simulates a total API-server outage for that cluster and should
    cause the remaining operators to detect the loss of leader heartbeats
    and elect a new leader.
    """
    leader = _state.get("initial_leader")
    assert leader, "initial leader was not captured in earlier test"

    # ``leader`` is the kube context name, typically ``kind-e2e-cluster-N``.
    # Strip the ``kind-`` prefix to derive the docker container name root
    # used by kind, then stop both the control-plane and worker nodes.
    ctx = leader[len("kind-") :] if leader.startswith("kind-") else leader

    subprocess.run(
        [
            "docker",
            "stop",
            f"kind-{ctx}-control-plane",
            f"kind-{ctx}-worker",
        ],
        check=True,
    )


@mark.e2e_multi_cluster_ha_failover
def test_new_leader_emerges(namespace: str):
    """Poll the leader CLI for up to 30s; assert that a new (different)
    leader has been elected."""
    initial_leader = _state["initial_leader"]
    deadline = time.monotonic() + 30.0
    new_leader = None
    last_err: Exception | None = None

    while time.monotonic() < deadline:
        try:
            candidate = _kubectl_mongodb_leader(namespace)
            if candidate and candidate != initial_leader:
                new_leader = candidate
                break
        except subprocess.CalledProcessError as e:
            last_err = e
        time.sleep(2)

    assert new_leader, (
        f"no new leader elected within 30s (initial leader was {initial_leader}; "
        f"last error: {last_err!r})"
    )
    _state["new_leader"] = new_leader
    print(f"New Raft leader after failover: {new_leader}")


@mark.e2e_multi_cluster_ha_failover
def test_apply_spec_change_to_new_leader(
    mongodb_multi: MongoDBMulti,
    namespace: str,
):
    """Apply a spec change (version bump) targeting the new leader cluster's
    API server and verify the CR reconciles to ``Running``."""
    new_leader = _state["new_leader"]
    assert new_leader, "new leader was not captured in earlier test"

    new_leader_client = get_member_cluster_api_client(new_leader)

    # Re-bind the CR's API client to the new leader and apply a small spec
    # change. Bumping featureCompatibilityVersion is a safe, low-impact way
    # to force a reconcile without changing process topology.
    mongodb_multi.api = kubernetes.client.CustomObjectsApi(new_leader_client)
    mongodb_multi.load()

    current_version = mongodb_multi["spec"].get("version", "")
    # Tweak any benign field; ``logLevel`` is always-accepted and triggers a
    # reconcile without affecting the running members.
    mongodb_multi["spec"]["logLevel"] = "DEBUG"
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)
    print(f"CR reconciled to Running on new leader {new_leader} (was version={current_version})")


@mark.e2e_multi_cluster_ha_failover
def test_post_failover_followers_pull_from_new_leader(
    namespace: str,
    member_cluster_names: list[str],
):
    """After failover, surviving followers' local replicas must carry the
    ``haraft.mongodb.com/replica-source`` annotation pointing at the NEW leader
    cluster (not the original leader whose kind containers were stopped).

    This confirms that the CRPuller restarted against the new leader after the
    Raft leadership-change callback fired, and has already mirrored at least one
    sync cycle to each surviving follower.
    """
    new_leader = _state["new_leader"]
    assert new_leader, "new leader was not captured in earlier test"

    # The killed cluster is still down; surviving clusters = all members except
    # the initial (killed) leader.
    initial_leader = _state["initial_leader"]
    surviving_followers = [c for c in member_cluster_names if c != initial_leader and c != new_leader]

    if not surviving_followers:
        # Only 2 clusters total (or all survivors became leaders): nothing
        # to assert, but this is unexpected for a 3-cluster setup.
        print(
            f"No surviving followers to check (new_leader={new_leader!r}, "
            f"initial_leader={initial_leader!r}, members={member_cluster_names!r})"
        )
        return

    # The CR was created by test_apply_cr_to_initial_leader above using the
    # mongodb_multi fixture whose name is "multi-replica-set" (see yaml_fixture).
    cr_name = "multi-replica-set"

    for follower in surviving_followers:
        api_client = get_member_cluster_api_client(follower)
        custom_api = kubernetes.client.CustomObjectsApi(api_client)

        # Allow a short window for the CRPuller's first post-failover sync.
        deadline = time.monotonic() + 60.0
        observed_source: str | None = None
        last_exc: Exception | None = None

        while time.monotonic() < deadline:
            try:
                obj = custom_api.get_namespaced_custom_object(
                    group=_CR_GROUP,
                    version=_CR_VERSION,
                    namespace=namespace,
                    plural=_CR_PLURAL,
                    name=cr_name,
                )
                annotations = (obj.get("metadata") or {}).get("annotations") or {}
                observed_source = annotations.get(_REPLICA_SOURCE_ANNOTATION)
                if observed_source == new_leader:
                    break
            except Exception as exc:  # noqa: BLE001
                last_exc = exc
            time.sleep(2)

        assert observed_source == new_leader, (
            f"follower {follower!r}: expected replica-source annotation "
            f"{new_leader!r} (new leader) after failover, but got "
            f"{observed_source!r} (last error: {last_exc!r})"
        )
        print(
            f"Follower {follower}: replica-source correctly updated to "
            f"new leader {new_leader!r} after failover."
        )
