import time
from typing import Set

import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_static_containers
from kubetester.mongodb import MongoDB
from pytest import fixture


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-liveness.yaml"), "my-replica-set", namespace)

    resource.update()

    return resource


def _get_pods(podname_template: str, qty: int = 3):
    return [podname_template.format(i) for i in range(qty)]


@skip_if_static_containers
@pytest.mark.e2e_replica_set_liveness_probe
@pytest.mark.flaky(reruns=10, reruns_delay=30)
def test_pods_are_running(replica_set: MongoDB, namespace: str):
    corev1_client = client.CoreV1Api()
    running_pods: Set[str] = set()
    # Wait for all the pods to be running
    # We can't wait for the replica set to be running
    # as it will never get to it (mongod is not starting)
    for podname in _get_pods("my-replica-set-{}", 3):
        pod = corev1_client.read_namespaced_pod(podname, namespace)
        if pod.status.phase == "Running":
            running_pods.add(podname)
    assert len(running_pods) == 3


@skip_if_static_containers
@pytest.mark.e2e_replica_set_liveness_probe
def test_no_pods_get_restarted(replica_set: MongoDB, namespace: str):
    corev1_client = client.CoreV1Api()
    statefulset_liveness_probe = replica_set.read_statefulset().spec.template.spec.containers[0].liveness_probe
    failure_threshold = statefulset_liveness_probe.failure_threshold
    period_seconds = statefulset_liveness_probe.period_seconds

    # Leave some extra time after the failure threshold just to be sure
    time.sleep(failure_threshold * period_seconds + 20)
    for podname in _get_pods("my-replica-set-{}", 3):
        pod = corev1_client.read_namespaced_pod(podname, namespace)

        # Pods should not restart because of a missing mongod process
        assert pod.status.container_statuses[0].restart_count == 0


@skip_if_static_containers
@pytest.mark.e2e_replica_set_liveness_probe
@pytest.mark.skip(
    reason="Liveness probe checks for mongod process to be up so killing the agent alone won't trigger a pod restart"
)
def test_pods_are_restarted_if_agent_process_is_terminated(replica_set: MongoDB, namespace: str):
    corev1_client = client.CoreV1Api()

    agent_pid_file = "/mongodb-automation/mongodb-mms-automation-agent.pid"
    pid_cmd = ["cat", agent_pid_file]
    # Get the agent's PID
    agent_pid = KubernetesTester.run_command_in_pod_container("my-replica-set-0", namespace, pid_cmd)

    # Kill the agent using its PID
    kill_cmd = ["kill", "-s", "SIGTERM", agent_pid.strip()]
    KubernetesTester.run_command_in_pod_container("my-replica-set-0", namespace, kill_cmd)

    # Ensure agent's pid file still exists.
    # This is to simulate not graceful kill, e.g. by OOM killer
    agent_pid_2 = KubernetesTester.run_command_in_pod_container("my-replica-set-0", namespace, pid_cmd)

    assert agent_pid == agent_pid_2

    statefulset_liveness_probe = replica_set.read_statefulset().spec.template.spec.containers[0].liveness_probe
    failure_threshold = statefulset_liveness_probe.failure_threshold
    period_seconds = statefulset_liveness_probe.period_seconds
    time.sleep(failure_threshold * period_seconds + 20)

    # Pod zero should have restarted, because the agent was killed
    assert (
        corev1_client.read_namespaced_pod("my-replica-set-0", namespace).status.container_statuses[0].restart_count > 0
    )

    # Pods 1 and 2 should not have restarted, because the agent is intact
    assert (
        corev1_client.read_namespaced_pod("my-replica-set-1", namespace).status.container_statuses[0].restart_count == 0
    )
    assert (
        corev1_client.read_namespaced_pod("my-replica-set-2", namespace).status.container_statuses[0].restart_count == 0
    )
