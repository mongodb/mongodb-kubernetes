import time
from typing import Set
import pytest
from kubetester.kubetester import (
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.mongodb import MongoDB
from pytest import fixture
from kubernetes import client


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-liveness.yaml"), "my-replica-set", namespace
    )

    resource.create()

    return resource


def _get_pods(podname_template: str, qty: int = 3):
    return [podname_template.format(i) for i in range(qty)]


@pytest.mark.e2e_replica_set_liveness_probe
def test_pods_are_running(replica_set: MongoDB, namespace: str):
    corev1_client = client.CoreV1Api()
    running_pods: Set[str] = set()
    tries = 10
    # Wait for all the pods to be running
    # We can't wait for the replica set to be running
    # as it will never get to it (mongod is not starting)
    while tries:
        if len(running_pods) == 3:
            break
        for podname in _get_pods("my-replica-set-{}", 3):
            try:
                pod = corev1_client.read_namespaced_pod(podname, namespace)
                if pod.status.phase == "Running":
                    running_pods.add(podname)
            except:
                # Pod not found, will retry
                pass
        tries -= 1
        time.sleep(30)
    assert len(running_pods) == 3


# test_pods_are_alive_first_5_mins makes sure that pods are not restarted in the first 5 mins.
# It does so by constantly checking the running time of PID 1 in each pod.
# If it is under 5 min, it asserts that the restart_count is 0
# When all pods are over 5 mins, it exists
@pytest.mark.e2e_replica_set_liveness_probe
def test_pods_are_alive_first_5_mins(replica_set: MongoDB, namespace: str):
    corev1_client = client.CoreV1Api()
    uptime_cmd = ["/bin/sh", "-c", "ps -o etimes= -p 1"]
    pods_over_5_mins: Set[str] = set()
    # Loop until all the pods are up for at least 5 mins
    # and check that no restart happens
    while True:
        if len(pods_over_5_mins) == 3:
            return
        for podname in _get_pods("my-replica-set-{}", 3):
            uptime = int(
                KubernetesTester.run_command_in_pod_container(
                    podname, namespace, uptime_cmd
                )
            )
            if uptime < 5 * 60:
                pod = corev1_client.read_namespaced_pod(podname, namespace)
                assert pod.status.container_statuses[0].restart_count == 0
            else:
                pods_over_5_mins.add(podname)


@pytest.mark.e2e_replica_set_liveness_probe
def test_pods_get_restarted(replica_set: MongoDB, namespace: str):
    corev1_client = client.CoreV1Api()
    statefulset_liveness_probe = (
        replica_set.read_statefulset().spec.template.spec.containers[0].liveness_probe
    )
    failure_threshold = statefulset_liveness_probe.failure_threshold
    period_seconds = statefulset_liveness_probe.period_seconds

    # Leave some extra time after the failure threshold just to be sure
    time.sleep(failure_threshold * period_seconds + 20)
    for podname in _get_pods("my-replica-set-{}", 3):
        pod = corev1_client.read_namespaced_pod(podname, namespace)

        # Pods should not restart because of a missing mongod process
        assert pod.status.container_statuses[0].restart_count == 0


@pytest.mark.e2e_replica_set_liveness_probe
def test_pods_are_restarted_if_agent_process_is_terminated(
    replica_set: MongoDB, namespace: str
):
    corev1_client = client.CoreV1Api()

    agent_pid_file = "/mongodb-automation/mongodb-mms-automation-agent.pid"
    pid_cmd = ["cat", agent_pid_file]
    # Get the agent's PID
    agent_pid = KubernetesTester.run_command_in_pod_container(
        "my-replica-set-0", namespace, pid_cmd
    )

    # Kill the agent using its PID
    kill_cmd = ["kill", "-s", "SIGTERM", agent_pid.strip()]
    KubernetesTester.run_command_in_pod_container(
        "my-replica-set-0", namespace, kill_cmd
    )

    # Remove PID file (not removed by agent after termination)
    rm_agent_pid_cmd = ["rm", agent_pid_file]
    KubernetesTester.run_command_in_pod_container(
        "my-replica-set-0", namespace, rm_agent_pid_cmd
    )

    statefulset_liveness_probe = (
        replica_set.read_statefulset().spec.template.spec.containers[0].liveness_probe
    )
    failure_threshold = statefulset_liveness_probe.failure_threshold
    period_seconds = statefulset_liveness_probe.period_seconds
    time.sleep(failure_threshold * period_seconds + 20)

    # Pod zero should have restarted, because the agent was killed
    assert (
        corev1_client.read_namespaced_pod("my-replica-set-0", namespace)
        .status.container_statuses[0]
        .restart_count
        > 0
    )

    # Pods 1 and 2 should not have restarted, because the agent is intact
    assert (
        corev1_client.read_namespaced_pod("my-replica-set-1", namespace)
        .status.container_statuses[0]
        .restart_count
        == 0
    )
    assert (
        corev1_client.read_namespaced_pod("my-replica-set-2", namespace)
        .status.container_statuses[0]
        .restart_count
        == 0
    )
