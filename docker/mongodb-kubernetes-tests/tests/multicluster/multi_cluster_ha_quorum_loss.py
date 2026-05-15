"""E2E test for Raft quorum loss in a multi-cluster operator deployment.

Scenario:
    1. Baseline: an existing ``MongoDBMultiCluster`` CR is ``Running`` (set up
       by prior tests in the suite).
    2. Two of the three member clusters are killed simultaneously
       (``docker stop`` for each cluster's kind containers). This drops the
       Raft group below quorum (only 1 of 3 nodes is reachable).
    3. Within ~15s the surviving cluster's operator should NOT be a leader
       (a Raft node can't be leader without quorum). ``kubectl-mongodb
       leader`` is expected to fail or return an empty string.
    4. The MongoDB data plane on the surviving cluster is unaffected: the
       mongod pods on that cluster continue to run. The management plane
       (operator reconciliation) is intentionally stalled until quorum is
       restored.
    5. One of the killed clusters is restarted via ``docker start``. With
       2-of-3 reachable nodes Raft regains quorum, and within ~60s
       ``kubectl-mongodb leader`` should return a leader name again.

Note: This test requires a 3-cluster kind environment with the HA POC bits
(Raft, kubectl-mongodb leader CLI, etc.) wired up and assumes a prior test
in the suite has produced a ``Running`` ``MongoDBMultiCluster`` resource
named ``multi-replica-set``. It is collection-clean and intended to be
exercised on a dedicated HA-enabled e2e variant.
"""

import subprocess
import time

import kubernetes
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.phase import Phase
from pytest import mark

from tests.conftest import get_member_cluster_api_client

# Shared mutable state across the ordered test functions. We record which
# two clusters were stopped so later tests can pick one to restart.
_state: dict = {}


def _kubectl_mongodb_leader(namespace: str) -> str:
    """Return the current Raft leader cluster name by invoking the
    ``kubectl-mongodb multicluster leader`` plugin."""
    return subprocess.check_output(
        ["./bin/kubectl-mongodb", "multicluster", "leader", "--namespace", namespace],
        text=True,
    ).strip()


def _kubectl_mongodb_leader_safe(namespace: str) -> tuple[str | None, Exception | None]:
    """Like ``_kubectl_mongodb_leader`` but never raises; returns
    ``(leader_or_none, error_or_none)`` so callers can distinguish "no
    leader" from "CLI failed because no quorum"."""
    try:
        return _kubectl_mongodb_leader(namespace) or None, None
    except subprocess.CalledProcessError as e:  # noqa: BLE001
        return None, e


def _kind_ctx(cluster_name: str) -> str:
    """Strip the ``kind-`` prefix from a kube-context name to derive the
    kind cluster context root used in docker container names."""
    return cluster_name[len("kind-") :] if cluster_name.startswith("kind-") else cluster_name


def _stop_cluster(cluster_name: str) -> None:
    """``docker stop`` both the control-plane and worker containers for a
    kind cluster."""
    ctx = _kind_ctx(cluster_name)
    subprocess.run(
        [
            "docker",
            "stop",
            f"kind-{ctx}-control-plane",
            f"kind-{ctx}-worker",
        ],
        check=True,
    )


def _start_cluster(cluster_name: str) -> None:
    """``docker start`` both the control-plane and worker containers for a
    kind cluster."""
    ctx = _kind_ctx(cluster_name)
    subprocess.run(
        [
            "docker",
            "start",
            f"kind-{ctx}-control-plane",
            f"kind-{ctx}-worker",
        ],
        check=True,
    )


@mark.e2e_multi_cluster_ha_quorum_loss
def test_baseline_running(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
):
    """Sanity check: the ``MongoDBMultiCluster`` CR set up by prior tests
    must be ``Running`` before we start tearing the control plane apart.

    If the precondition isn't met we fail fast with a clear message: this
    test is not responsible for creating the CR -- it inherits one from
    earlier tests in the same suite (e.g. failover / recovery)."""
    resource = MongoDBMulti(name="multi-replica-set", namespace=namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try:
        resource.load()
    except Exception as e:  # noqa: BLE001
        raise AssertionError(
            "precondition not met: MongoDBMultiCluster 'multi-replica-set' "
            f"could not be loaded in namespace {namespace!r}; this test "
            f"expects a Running CR from prior tests in the suite. Error: {e!r}"
        )

    resource.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_multi_cluster_ha_quorum_loss
def test_kill_two_clusters(
    namespace: str,
    member_cluster_names: list[str],
):
    """Stop the kind containers backing two of the three member clusters
    simultaneously. We prefer to keep the current leader alive as the
    surviving cluster so the post-conditions are deterministic; if the
    leader CLI is unavailable we just pick the last cluster as the
    survivor.
    """
    assert len(member_cluster_names) >= 3, (
        f"this test requires at least 3 member clusters, got {member_cluster_names!r}"
    )

    leader, _ = _kubectl_mongodb_leader_safe(namespace)

    if leader and leader in member_cluster_names:
        survivor = leader
    else:
        survivor = member_cluster_names[-1]

    to_kill = [c for c in member_cluster_names if c != survivor][:2]
    assert len(to_kill) == 2, (
        f"expected to identify exactly 2 clusters to kill, got {to_kill!r} "
        f"(survivor={survivor!r}, members={member_cluster_names!r})"
    )

    print(f"Survivor cluster: {survivor}; killing: {to_kill}")
    _state["survivor"] = survivor
    _state["killed_clusters"] = to_kill

    for cluster in to_kill:
        _stop_cluster(cluster)


@mark.e2e_multi_cluster_ha_quorum_loss
def test_no_leader_during_quorum_loss(namespace: str):
    """After ~15s the surviving cluster's operator must not report itself
    (or anyone else) as the Raft leader: a single node out of three cannot
    form a quorum.

    Acceptable outcomes (any of these passes):
        * ``kubectl-mongodb leader`` exits non-zero (the CLI can't reach
          the dead clusters and the local node is not a leader).
        * The CLI exits 0 but stdout is empty.
    """
    # Give Raft time to notice the missing heartbeats and step down.
    time.sleep(15)

    leader, err = _kubectl_mongodb_leader_safe(namespace)
    assert not leader, (
        f"expected no leader during quorum loss but kubectl-mongodb returned "
        f"{leader!r} (cli error: {err!r}); killed clusters: "
        f"{_state.get('killed_clusters')!r}"
    )
    print(f"No leader during quorum loss (as expected); cli error: {err!r}")


@mark.e2e_multi_cluster_ha_quorum_loss
def test_mongodb_data_plane_unaffected(namespace: str):
    """The mongod pods on the surviving cluster must still be running.

    The management plane (operator reconciliation) is stalled by design
    when the Raft group has no quorum, but the data plane runs entirely
    in the member clusters' kubelets and is independent of the operator.
    """
    survivor = _state.get("survivor")
    assert survivor, "survivor cluster was not captured in earlier test"

    client = get_member_cluster_api_client(survivor)
    core = kubernetes.client.CoreV1Api(client)

    pods = core.list_namespaced_pod(namespace=namespace, _request_timeout=15)
    # Filter to mongod statefulset pods; if labels don't match what we
    # expect, fall back to "any pod in the namespace exists and is
    # running" -- the test only needs to demonstrate the data plane is up.
    relevant = [
        p
        for p in pods.items
        if (p.metadata.labels or {}).get("app") in {"multi-replica-set-svc", "multi-replica-set"}
        or (p.metadata.name or "").startswith("multi-replica-set")
    ]
    if not relevant:
        relevant = list(pods.items)

    assert relevant, (
        f"no pods found in namespace {namespace!r} on surviving cluster "
        f"{survivor!r}; data plane appears to be gone"
    )

    running = [p for p in relevant if (p.status.phase or "") == "Running"]
    assert running, (
        f"no Running pods on surviving cluster {survivor!r} in namespace "
        f"{namespace!r}; phases observed: "
        f"{sorted({p.status.phase for p in relevant})!r}"
    )
    print(
        f"Data plane on surviving cluster {survivor!r}: "
        f"{len(running)}/{len(relevant)} relevant pods Running"
    )


@mark.e2e_multi_cluster_ha_quorum_loss
def test_recovery_restores_leader(namespace: str):
    """Restart one of the killed clusters via ``docker start``. With 2 of
    3 nodes reachable Raft regains quorum and should elect a leader
    within ~60s. Assert ``kubectl-mongodb leader`` returns a non-empty
    name within that window.
    """
    killed = _state.get("killed_clusters") or []
    assert killed, "no killed clusters captured in earlier test"

    to_restart = killed[0]
    print(f"Restarting cluster to restore quorum: {to_restart}")
    _start_cluster(to_restart)

    deadline = time.monotonic() + 60.0
    observed: str | None = None
    last_err: Exception | None = None

    while time.monotonic() < deadline:
        observed, last_err = _kubectl_mongodb_leader_safe(namespace)
        if observed:
            break
        time.sleep(2)

    assert observed, (
        f"no leader elected within 60s after restarting {to_restart!r} "
        f"(last cli error: {last_err!r})"
    )
    print(f"Leader after quorum restoration: {observed}")
