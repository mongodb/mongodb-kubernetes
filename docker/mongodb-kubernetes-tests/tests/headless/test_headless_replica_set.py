"""
End-to-end test for a MongoDB replica set that is not managed by Ops Manager.

The resource has neither spec.opsManager, nor spec.cloudManager, nor spec.credentials, and is
annotated with the static architecture. The operator must store the automation config in the
<name>-config Secret and the agents must run in headless mode.

    pytest -m e2e_mdb_headless_replica_set
"""

import json
from typing import Any, Dict

import kubernetes.client
import pytest
from kubetester import read_secret, try_load, wait_until
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ReplicaSetTester
from kubetester.phase import Phase
from pytest import fixture
from tests.pod_logs import get_structured_json_pod_logs

RESOURCE_NAME = "headless-rs"
MEMBERS = 3

AC_SECRET_SUFFIX = "-config"
AC_SECRET_KEY = "cluster-config.json"
AGENT_CONTAINER_NAME = "mongodb-agent"
HEADLESS_AGENT_VERSION_ANNOTATION = "agent.mongodb.com/version"
HEADLESS_LOG_MESSAGE = 'Mongodb Agent is configured to run in "headless" mode using local config file'
CLUSTER_CONFIG_FLAG = "-cluster=/var/lib/mongodb-automation/cluster-config.json"

pytestmark = pytest.mark.e2e_mdb_headless_replica_set


@fixture(scope="module")
def headless_replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB(namespace=namespace, name=RESOURCE_NAME)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"] = {
        "type": "ReplicaSet",
        "members": MEMBERS,
        "version": custom_mdb_version,
        "persistent": False,
        "security": {"authentication": {"enabled": False}},
    }

    try_load(resource)
    # Known starting point, even if a previous run left the resource scaled up.
    resource["spec"]["members"] = MEMBERS
    return resource


def _read_automation_config(namespace: str) -> Dict[str, Any]:
    secret = read_secret(namespace, f"{RESOURCE_NAME}{AC_SECRET_SUFFIX}")
    assert AC_SECRET_KEY in secret
    return json.loads(secret[AC_SECRET_KEY])


def _agent_container(pod: kubernetes.client.V1Pod) -> kubernetes.client.V1Container:
    return next(container for container in pod.spec.containers if container.name == AGENT_CONTAINER_NAME)


def _agent_version_annotation(namespace: str, pod_name: str) -> str:
    pod = kubernetes.client.CoreV1Api().read_namespaced_pod(pod_name, namespace)
    annotations = pod.metadata.annotations or {}
    return annotations.get(HEADLESS_AGENT_VERSION_ANNOTATION, "")


def test_headless_replica_set_reaches_running(headless_replica_set: MongoDB):
    headless_replica_set.update()
    headless_replica_set.assert_reaches_phase(Phase.Running, timeout=900)


def test_automation_config_is_stored_in_a_secret(headless_replica_set: MongoDB, namespace: str):
    ac = _read_automation_config(namespace)

    assert ac["version"] >= 1
    assert len(ac["processes"]) == MEMBERS
    assert sorted(process["name"] for process in ac["processes"]) == [
        f"{RESOURCE_NAME}-{idx}" for idx in range(MEMBERS)
    ]
    assert not ac.get("monitoringVersions")
    assert not ac.get("backupVersions")


def test_agent_pods_are_headless_and_report_automation_config_version(headless_replica_set: MongoDB, namespace: str):
    version = str(_read_automation_config(namespace)["version"])

    for idx in range(MEMBERS):
        pod_name = f"{RESOURCE_NAME}-{idx}"
        pod = kubernetes.client.CoreV1Api().read_namespaced_pod(pod_name, namespace)

        annotations = pod.metadata.annotations or {}
        assert annotations.get(HEADLESS_AGENT_VERSION_ANNOTATION) == version

        container = _agent_container(pod)
        env = {env_var.name: env_var.value for env_var in (container.env or [])}
        assert env.get("HEADLESS_AGENT") == "true"
        assert not env.get("BASE_URL")
        assert env.get("AUTOMATION_CONFIG_MAP") == f"{RESOURCE_NAME}{AC_SECRET_SUFFIX}"
        assert container.readiness_probe is not None


def test_agent_launcher_runs_agent_with_local_config_file(headless_replica_set: MongoDB, namespace: str):
    for idx in range(MEMBERS):
        pod_name = f"{RESOURCE_NAME}-{idx}"
        logs_by_type = get_structured_json_pod_logs(namespace, pod_name, AGENT_CONTAINER_NAME)
        launcher_logs = "\n".join(logs_by_type.get("agent-launcher-script", []))

        assert HEADLESS_LOG_MESSAGE in launcher_logs
        assert CLUSTER_CONFIG_FLAG in launcher_logs


def test_replica_set_is_reachable_and_formed(headless_replica_set: MongoDB, namespace: str, cluster_domain: str):
    tester = ReplicaSetTester(RESOURCE_NAME, MEMBERS, ssl=False, namespace=namespace, cluster_domain=cluster_domain)
    tester.assert_connectivity()

    hello = tester.client.admin.command("hello")
    assert hello["ok"] == 1
    assert hello["setName"] == RESOURCE_NAME
    assert len(hello["hosts"]) == MEMBERS


def test_scaling_up_bumps_automation_config_and_updates_pod_annotations(headless_replica_set: MongoDB, namespace: str):
    headless_replica_set.load()
    old_version = _read_automation_config(namespace)["version"]

    headless_replica_set["spec"]["members"] = MEMBERS + 1
    headless_replica_set.update()
    headless_replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    ac = _read_automation_config(namespace)
    assert len(ac["processes"]) == MEMBERS + 1
    assert ac["version"] > old_version
    new_version = str(ac["version"])

    def all_pods_report_new_version() -> bool:
        for idx in range(MEMBERS + 1):
            if _agent_version_annotation(namespace, f"{RESOURCE_NAME}-{idx}") != new_version:
                return False
        return True

    wait_until(
        all_pods_report_new_version,
        timeout=300,
        msg="all agent pods to report the new automation config version",
    )


def test_deleting_resource_garbage_collects_automation_config_secret(headless_replica_set: MongoDB, namespace: str):
    secret_name = f"{RESOURCE_NAME}{AC_SECRET_SUFFIX}"
    headless_replica_set.delete()

    def secret_is_deleted() -> bool:
        try:
            read_secret(namespace, secret_name)
            return False
        except kubernetes.client.ApiException as e:
            return e.status == 404

    wait_until(secret_is_deleted, timeout=180, msg=f"automation config secret {secret_name} to be deleted")
