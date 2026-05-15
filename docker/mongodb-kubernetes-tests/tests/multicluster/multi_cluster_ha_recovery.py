"""E2E test for Raft-based HA recovery in a multi-cluster operator deployment.

Scenario (runs AFTER ``multi_cluster_ha_failover.py``):
    1. The originally-failed cluster (whose kind containers were ``docker
       stop``-ped in the failover test) is restarted via ``docker start``.
    2. We verify that the leader does NOT change: the recovered cluster
       should rejoin the Raft group as a FOLLOWER, not preempt the current
       leader.
    3. A spec change is applied to the current leader.
    4. We verify the recovered cluster eventually sees the updated CR on
       its own API server (i.e. the change propagated from the leader).

Note: This test requires a 3-cluster kind environment with the HA POC bits
(Raft, kubectl-mongodb leader CLI, etc.) wired up AND assumes one cluster
was previously ``docker stop``-ped by the failover test. It is
collection-clean and intended to be exercised on a dedicated HA-enabled
e2e variant.
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

# Shared mutable state across the ordered test functions. We record the
# pre-recovery leader and the name of the cluster being recovered so later
# tests can target the right API servers.
_state: dict = {}


def _kubectl_mongodb_leader(namespace: str) -> str:
    """Return the current Raft leader cluster name by invoking the
    ``kubectl-mongodb multicluster leader`` plugin."""
    return subprocess.check_output(
        ["./bin/kubectl-mongodb", "multicluster", "leader", "--namespace", namespace],
        text=True,
    ).strip()


def _kind_ctx(cluster_name: str) -> str:
    """Strip the ``kind-`` prefix from a kube-context name to derive the
    kind cluster context root used in docker container names."""
    return cluster_name[len("kind-") :] if cluster_name.startswith("kind-") else cluster_name


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


@mark.e2e_multi_cluster_ha_recovery
def test_deploy_operator(multi_cluster_operator: Operator):
    """Sanity-check that the operator is up before exercising recovery."""
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_ha_recovery
def test_capture_pre_recovery_leader(
    namespace: str,
    member_cluster_names: list[str],
):
    """Record the current Raft leader and figure out which cluster is the
    one that was previously stopped (the cluster which is NOT the current
    leader and whose API server is currently unreachable).

    We use a simple heuristic: the killed cluster is the member cluster
    that is not the current leader and whose kind control-plane container
    is currently in a non-running state. In practice the failover test
    only stops one cluster's containers, so we just identify the one
    member cluster name whose docker container is reported as ``exited``.
    """
    pre_recovery_leader = _kubectl_mongodb_leader(namespace)
    assert pre_recovery_leader, "kubectl-mongodb returned an empty leader name"
    _state["pre_recovery_leader"] = pre_recovery_leader
    print(f"Pre-recovery Raft leader: {pre_recovery_leader}")

    killed_cluster: str | None = None
    for name in member_cluster_names:
        ctx = _kind_ctx(name)
        container = f"kind-{ctx}-control-plane"
        try:
            status = subprocess.check_output(
                ["docker", "inspect", "-f", "{{.State.Status}}", container],
                text=True,
            ).strip()
        except subprocess.CalledProcessError:
            continue
        if status != "running":
            killed_cluster = name
            break

    assert killed_cluster, (
        "could not identify a stopped member cluster; this test expects to run "
        "after multi_cluster_ha_failover.py which leaves one cluster's kind "
        "containers in the 'exited' state"
    )
    assert killed_cluster != pre_recovery_leader, (
        f"the stopped cluster ({killed_cluster}) is also the current leader; "
        "failover did not occur"
    )
    _state["killed_cluster"] = killed_cluster
    print(f"Killed cluster to recover: {killed_cluster}")


@mark.e2e_multi_cluster_ha_recovery
def test_restart_failed_cluster():
    """``docker start`` the killed cluster's kind containers and wait up
    to 60s for the kubelet (via the kube API server) to respond.
    """
    killed = _state.get("killed_cluster")
    assert killed, "killed cluster was not captured in earlier test"

    ctx = _kind_ctx(killed)
    subprocess.run(
        [
            "docker",
            "start",
            f"kind-{ctx}-control-plane",
            f"kind-{ctx}-worker",
        ],
        check=True,
    )

    # Wait up to 60s for the API server in the recovered cluster to start
    # responding. We poll by issuing a lightweight ``kubectl get --raw``
    # against the cluster's API via its kube client.
    deadline = time.monotonic() + 60.0
    last_err: Exception | None = None
    while time.monotonic() < deadline:
        try:
            client = get_member_cluster_api_client(killed)
            core = kubernetes.client.CoreV1Api(client)
            core.list_namespace(_request_timeout=5)
            print(f"Recovered cluster {killed} API server is responsive")
            return
        except Exception as e:  # noqa: BLE001
            last_err = e
            time.sleep(2)

    raise AssertionError(
        f"recovered cluster {killed} API server did not become responsive "
        f"within 60s (last error: {last_err!r})"
    )


@mark.e2e_multi_cluster_ha_recovery
def test_leader_unchanged_after_recovery(namespace: str):
    """Verify the leader did NOT change as a result of the recovery: the
    recovered cluster must rejoin as a follower.

    We allow a short settle window for the recovered operator's Raft
    state to converge, then poll for ~30s to confirm the leader is stable
    and equal to the pre-recovery leader (and is not the originally-
    killed cluster).
    """
    pre_recovery_leader = _state["pre_recovery_leader"]
    killed = _state["killed_cluster"]

    deadline = time.monotonic() + 30.0
    observed: str | None = None
    last_err: Exception | None = None

    while time.monotonic() < deadline:
        try:
            observed = _kubectl_mongodb_leader(namespace)
        except subprocess.CalledProcessError as e:
            last_err = e
            time.sleep(2)
            continue
        # Fail fast if the leader unexpectedly changes to the recovered cluster.
        assert observed != killed, (
            f"recovered cluster {killed} became the leader; expected it to "
            f"rejoin as a follower (pre-recovery leader was {pre_recovery_leader})"
        )
        if observed == pre_recovery_leader:
            time.sleep(2)
            continue
        # Some other cluster reports as leader -- that's an unexpected change.
        raise AssertionError(
            f"leader changed during recovery: pre={pre_recovery_leader}, "
            f"now={observed}, killed={killed}"
        )

    assert observed == pre_recovery_leader, (
        f"could not confirm stable leader after recovery (observed={observed!r}, "
        f"pre={pre_recovery_leader}, last error: {last_err!r})"
    )
    print(f"Leader unchanged after recovery: {pre_recovery_leader}")


@mark.e2e_multi_cluster_ha_recovery
def test_new_spec_change_propagates_to_recovered_cluster(
    mongodb_multi: MongoDBMulti,
    namespace: str,
):
    """Patch the CR on the current leader, then verify the recovered
    cluster eventually sees the updated ``spec.logLevel`` on its own API
    server (i.e. Raft replicated the change to the rejoined follower).
    """
    leader = _state["pre_recovery_leader"]
    killed = _state["killed_cluster"]

    leader_client = get_member_cluster_api_client(leader)
    mongodb_multi.api = kubernetes.client.CustomObjectsApi(leader_client)
    mongodb_multi.load()

    # Pick a log level different from whatever the failover test set
    # (which used "DEBUG"). Using "INFO" guarantees a change either way.
    target_log_level = "INFO" if mongodb_multi["spec"].get("logLevel") == "DEBUG" else "DEBUG"
    mongodb_multi["spec"]["logLevel"] = target_log_level
    mongodb_multi.update()

    # Wait for the CR to reconcile on the leader first.
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)

    # Now poll the recovered cluster's API server until it sees the same
    # spec.logLevel. Allow up to 5 minutes for Raft replication + watch
    # propagation in case the follower is still catching up.
    recovered_client = get_member_cluster_api_client(killed)
    recovered_api = kubernetes.client.CustomObjectsApi(recovered_client)

    deadline = time.monotonic() + 300.0
    observed_level: str | None = None
    last_err: Exception | None = None

    while time.monotonic() < deadline:
        try:
            obj = recovered_api.get_namespaced_custom_object(
                group="mongodb.com",
                version="v1",
                namespace=namespace,
                plural="mongodbmulticluster",
                name=mongodb_multi.name,
            )
            observed_level = obj.get("spec", {}).get("logLevel")
            if observed_level == target_log_level:
                print(
                    f"Recovered cluster {killed} observed propagated "
                    f"spec.logLevel={target_log_level}"
                )
                return
        except Exception as e:  # noqa: BLE001
            last_err = e
        time.sleep(5)

    raise AssertionError(
        f"recovered cluster {killed} did not observe propagated "
        f"spec.logLevel={target_log_level} within 5 minutes "
        f"(observed={observed_level!r}, last error: {last_err!r})"
    )
