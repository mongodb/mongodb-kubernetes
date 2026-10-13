"""
End-to-end test: a headless MongoDB replica set handed over to Ops Manager.

The resource starts without spec.opsManager / spec.cloudManager / spec.credentials: the operator
stores the automation config in the <name>-config Secret and the agents run against that file
(the agent container carries HEADLESS_AGENT=true). Adding the Ops Manager connection configuration
and credentials in one update must migrate the deployment without losing data and without
restarting any agent before the automation config exists in Ops Manager.

CI runs this against the cloud-QA Ops Manager (same path as the other cloudqa tests). Set
HEADLESS_TO_OM_LOCAL_OM=true to run against a small in-cluster Ops Manager instead (local
verification when no cloud-QA credentials are available).

    pytest -m e2e_mdb_headless_to_om
"""

import json
import os
import time
import uuid
from typing import Any, Dict, List, Optional

import kubernetes.client
import pytest
from kubetester import read_configmap, read_secret, try_load, wait_until
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ReplicaSetTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture

RESOURCE_NAME = "headless-to-om"
# A unique per-run project name keeps local re-runs (and CI task retries in a reused namespace)
# from colliding with the project of a previous attempt.
PROJECT_NAME = os.getenv("HEADLESS_TO_OM_PROJECT_NAME", f"headless-to-om-{uuid.uuid4().hex[:8]}")
MEMBERS = 3

AC_SECRET_SUFFIX = "-config"
AC_SECRET_KEY = "cluster-config.json"
AGENT_CONTAINER_NAME = "mongodb-agent"
HEADLESS_AGENT_ENV = "HEADLESS_AGENT"
HEADLESS_AGENT_VERSION_ANNOTATION = "agent.mongodb.com/version"
HEADLESS_AUTOMATION_CONFIG_VOLUME = "automation-config"
MIGRATION_ANNOTATION = "mongodb.com/v1.headless-migration"
MIGRATION_IN_PROGRESS = "in-progress"

CLOUD_QA_PROJECT_CONFIG_MAP = "my-project"
CLOUD_QA_CREDENTIALS_SECRET = "my-credentials"
OM_ADMIN_SECRET = "ops-manager-admin-secret"
LOCAL_OM_ENV_VAR = "HEADLESS_TO_OM_LOCAL_OM"

TEST_DB = "headless-migration"
TEST_COLLECTION = "documents"

pytestmark = pytest.mark.e2e_mdb_headless_to_om

# Pod UIDs captured before the migration, asserted in a later test to prove every pod was rolled.
STATE: Dict[str, Any] = {}


@fixture(scope="module")
def headless_replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    """A headless replica set, re-created from scratch if a previous run already migrated it."""
    resource = _new_headless_replica_set(namespace, custom_mdb_version)
    try_load(resource)
    if not _is_headless_spec(resource):
        resource.delete()
        wait_until(
            lambda: not _resource_exists(resource),
            timeout=300,
            msg=f"MongoDB {RESOURCE_NAME} from a previous run to be deleted",
        )
        resource = _new_headless_replica_set(namespace, custom_mdb_version)
    return resource


@fixture(scope="module")
def ops_manager(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str
) -> Optional[MongoDBOpsManager]:
    """Optional in-cluster Ops Manager.

    The cloud-QA variants use the external Ops Manager described by my-project/my-credentials
    (created by scripts/evergreen/e2e/configure_operator.sh). Set HEADLESS_TO_OM_LOCAL_OM=true to
    deploy a small in-cluster Ops Manager instead - only for local verification without cloud-QA
    credentials. CI never sets the variable, so the fixture is a no-op there.
    """
    if os.getenv(LOCAL_OM_ENV_VAR, "") != "true":
        return None

    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    if not _secret_exists(namespace, OM_ADMIN_SECRET):
        resource.create_admin_secret()

    try_load(resource)
    resource.update()
    resource.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    resource.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    return resource


def test_headless_replica_set_reaches_running(headless_replica_set: MongoDB, namespace: str):
    headless_replica_set.update()
    headless_replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    automation_config = _read_automation_config(headless_replica_set)
    assert automation_config["version"] >= 1
    assert len(automation_config["processes"]) == MEMBERS

    assert sorted(_headless_pod_names(namespace)) == sorted(_expected_pod_names())


def test_write_data_before_migration(headless_replica_set: MongoDB, namespace: str, cluster_domain: str):
    tester = ReplicaSetTester(RESOURCE_NAME, MEMBERS, ssl=False, namespace=namespace, cluster_domain=cluster_domain)
    tester.assert_connectivity()
    tester.client[TEST_DB][TEST_COLLECTION].replace_one({"_id": "before-migration"}, {"value": 42}, upsert=True)


def test_seed_in_ops_manager_happens_before_any_pod_rolls(
    headless_replica_set: MongoDB, ops_manager: Optional[MongoDBOpsManager], namespace: str
):
    _assert_connection_resources_exist(ops_manager, namespace)

    STATE["pod_uids_before"] = {
        pod_name: _read_pod(namespace, pod_name).metadata.uid for pod_name in _expected_pod_names()
    }

    headless_replica_set.load()
    _configure_ops_manager_target(headless_replica_set, ops_manager)
    headless_replica_set.update()

    # The operator writes the marker before it creates/seeds the Ops Manager project and before it
    # flips the StatefulSet template.
    wait_until(
        lambda: _migration_marker_is_set(headless_replica_set),
        timeout=300,
        sleep_time=2,
        msg=f"{MIGRATION_ANNOTATION}={MIGRATION_IN_PROGRESS} to appear on {RESOURCE_NAME}",
    )
    sts = headless_replica_set.read_statefulset()
    updated_replicas_at_marker = sts.status.updated_replicas

    # Ordering invariant: the automation config must be observed in Ops Manager while every pod
    # still runs the headless agent. If an agent had been restarted before the seed, we would see
    # fewer headless pods (or a pod without HEADLESS_AGENT) with no automation config in OM.
    processes_in_ops_manager: Optional[List[str]] = None
    deadline = time.time() + 300
    while time.time() < deadline:
        headless_replica_set.reload()
        if headless_replica_set.get_status_phase() == Phase.Failed:
            pytest.fail(
                "the resource failed while migrating from headless mode to Ops Manager: "
                f"{headless_replica_set.get_status_message()}"
            )

        processes_in_ops_manager = _ops_manager_process_names(headless_replica_set)
        if processes_in_ops_manager is not None and len(processes_in_ops_manager) >= MEMBERS:
            break

        headless_pods = _headless_pod_names(namespace)
        assert len(headless_pods) == MEMBERS, (
            "a pod was rolled to the online agent before the automation config reached Ops Manager: "
            f"headless pods={headless_pods}, processes in Ops Manager={processes_in_ops_manager}"
        )
        time.sleep(2)

    assert processes_in_ops_manager is not None, (
        f"the headless automation config was not seeded into Ops Manager within 300s "
        f"(StatefulSet updatedReplicas at marker time: {updated_replicas_at_marker})"
    )
    assert set(processes_in_ops_manager) == set(_expected_pod_names())
    assert len(_headless_pod_names(namespace)) > 0, (
        "the automation config appeared in Ops Manager only after the rollout had finished; the seed "
        "must happen before the StatefulSet pod template flips to the online agent"
    )


def test_migration_completes_with_ops_manager_managed_pods(headless_replica_set: MongoDB, namespace: str):
    headless_replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    headless_replica_set.reload()
    annotations = headless_replica_set["metadata"].get("annotations") or {}
    assert MIGRATION_ANNOTATION not in annotations, "the migration marker must be cleared on completion"

    for pod_name in _expected_pod_names():
        pod = _read_pod(namespace, pod_name)
        env = _agent_env(pod)
        assert env.get(HEADLESS_AGENT_ENV) != "true", f"pod {pod_name} still runs the headless agent"
        assert env.get("BASE_URL"), f"pod {pod_name} is not configured with the Ops Manager BASE_URL"
        assert HEADLESS_AGENT_VERSION_ANNOTATION not in (
            pod.metadata.annotations or {}
        ), f"pod {pod_name} still carries the headless automation config version annotation"

    def all_pods_are_online() -> bool:
        sts = headless_replica_set.read_statefulset()
        volume_names = {volume.name for volume in sts.spec.template.spec.volumes}
        return (
            sts.status.updated_replicas == MEMBERS
            and sts.status.ready_replicas == MEMBERS
            and HEADLESS_AUTOMATION_CONFIG_VOLUME not in volume_names
        )

    wait_until(all_pods_are_online, timeout=300, msg="all pods to run the online agent")

    om_tester = headless_replica_set.get_om_tester()
    project_id = om_tester.context.project_id
    assert headless_replica_set["status"]["projectId"] == project_id
    link = headless_replica_set["status"]["link"]
    assert link and project_id in link, f"expected an Ops Manager link for project {project_id}, got {link!r}"

    automation_config = om_tester.get_automation_config_tester().automation_config
    assert {process["name"] for process in automation_config["processes"]} == set(_expected_pod_names())

    # automationConfig does not expose lastGoalVersionAchieved (Ops Manager omits it from GET and
    # rejects it on PUT); automationStatus is the API that reports it.
    automation_status = om_tester.api_get_automation_status()
    assert {process["name"] for process in automation_status["processes"]} == set(_expected_pod_names())
    for process in automation_status["processes"]:
        assert process["lastGoalVersionAchieved"] == automation_status["goalVersion"], (
            f"process {process['name']} is at version {process['lastGoalVersionAchieved']}, "
            f"goal is {automation_status['goalVersion']}"
        )


def test_all_pods_were_rolled_to_the_online_agent(headless_replica_set: MongoDB, namespace: str):
    pod_uids_before = STATE.get("pod_uids_before")
    assert pod_uids_before, "pod UIDs must have been captured before the migration"

    for pod_name in _expected_pod_names():
        assert (
            _read_pod(namespace, pod_name).metadata.uid != pod_uids_before[pod_name]
        ), f"pod {pod_name} was not rolled during the migration"


def test_data_written_before_migration_is_preserved(namespace: str, cluster_domain: str):
    tester = ReplicaSetTester(RESOURCE_NAME, MEMBERS, ssl=False, namespace=namespace, cluster_domain=cluster_domain)
    tester.assert_connectivity()

    document = tester.client[TEST_DB][TEST_COLLECTION].find_one({"_id": "before-migration"})
    assert document is not None, "data written before the migration is gone after the rolling restart"
    assert document["value"] == 42


def _new_headless_replica_set(namespace: str, version: str) -> MongoDB:
    resource = MongoDB(namespace=namespace, name=RESOURCE_NAME)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"] = {
        "type": "ReplicaSet",
        "members": MEMBERS,
        "version": version,
        "persistent": True,
        "security": {"authentication": {"enabled": False}},
    }
    return resource


def _is_headless_spec(resource: MongoDB) -> bool:
    return "opsManager" not in resource["spec"] and "cloudManager" not in resource["spec"]


def _configure_ops_manager_target(resource: MongoDB, ops_manager: Optional[MongoDBOpsManager]) -> None:
    if ops_manager is not None:
        resource.configure_ops_manager(ops_manager, PROJECT_NAME)
    else:
        resource.configure_cloud_qa(PROJECT_NAME)
        resource["spec"]["credentials"] = CLOUD_QA_CREDENTIALS_SECRET


def _assert_connection_resources_exist(ops_manager: Optional[MongoDBOpsManager], namespace: str) -> None:
    if ops_manager is not None:
        return

    try:
        project_config = read_configmap(namespace, CLOUD_QA_PROJECT_CONFIG_MAP)
    except kubernetes.client.ApiException as e:
        if e.status != 404:
            raise
        pytest.fail(
            f"ConfigMap '{CLOUD_QA_PROJECT_CONFIG_MAP}' with the Ops Manager connection details was not "
            f"found in namespace '{namespace}'. In CI it is created by "
            "scripts/evergreen/e2e/configure_operator.sh from OM_BASE_URL/OM_ORGID; locally export "
            "OM_BASE_URL/OM_USER/OM_API_KEY/OM_ORGID or set HEADLESS_TO_OM_LOCAL_OM=true to deploy an "
            "in-cluster Ops Manager."
        )

    if not project_config.get("baseUrl"):
        pytest.fail(
            f"ConfigMap '{CLOUD_QA_PROJECT_CONFIG_MAP}' in namespace '{namespace}' has no 'baseUrl'; "
            "check OM_BASE_URL / the Ops Manager connection configuration"
        )

    try:
        credentials = read_secret(namespace, CLOUD_QA_CREDENTIALS_SECRET)
    except kubernetes.client.ApiException as e:
        if e.status != 404:
            raise
        pytest.fail(
            f"Secret '{CLOUD_QA_CREDENTIALS_SECRET}' with the Ops Manager API credentials was not found "
            f"in namespace '{namespace}'. In CI it is created by scripts/evergreen/e2e/configure_operator.sh "
            "from OM_USER/OM_API_KEY; locally export those variables or set HEADLESS_TO_OM_LOCAL_OM=true "
            "to deploy an in-cluster Ops Manager."
        )

    if not credentials.get("publicApiKey") and not credentials.get("privateKey"):
        pytest.fail(
            f"Secret '{CLOUD_QA_CREDENTIALS_SECRET}' does not contain an Ops Manager API key; "
            "check OM_USER/OM_API_KEY in the environment"
        )


def _resource_exists(resource: MongoDB) -> bool:
    try:
        resource.load()
        return True
    except kubernetes.client.ApiException as e:
        if e.status == 404:
            return False
        raise


def _secret_exists(namespace: str, name: str) -> bool:
    try:
        kubernetes.client.CoreV1Api().read_namespaced_secret(name, namespace)
        return True
    except kubernetes.client.ApiException as e:
        if e.status == 404:
            return False
        raise


def _expected_pod_names() -> List[str]:
    return [f"{RESOURCE_NAME}-{idx}" for idx in range(MEMBERS)]


def _read_pod(namespace: str, name: str) -> kubernetes.client.V1Pod:
    return kubernetes.client.CoreV1Api().read_namespaced_pod(name, namespace)


def _agent_env(pod: kubernetes.client.V1Pod) -> Dict[str, Optional[str]]:
    agent = next(container for container in pod.spec.containers if container.name == AGENT_CONTAINER_NAME)
    return {env_var.name: env_var.value for env_var in (agent.env or [])}


def _headless_pod_names(namespace: str) -> List[str]:
    return [
        pod_name
        for pod_name in _expected_pod_names()
        if _agent_env(_read_pod(namespace, pod_name)).get(HEADLESS_AGENT_ENV) == "true"
    ]


def _read_automation_config(resource: MongoDB) -> Dict[str, Any]:
    secret = read_secret(resource.namespace, f"{resource.name}{AC_SECRET_SUFFIX}")
    assert AC_SECRET_KEY in secret, f"secret {resource.name}{AC_SECRET_SUFFIX} has no {AC_SECRET_KEY} key"
    return json.loads(secret[AC_SECRET_KEY])


def _migration_marker_is_set(resource: MongoDB) -> bool:
    resource.reload()
    annotations = resource["metadata"].get("annotations") or {}
    return annotations.get(MIGRATION_ANNOTATION) == MIGRATION_IN_PROGRESS


def _ops_manager_process_names(resource: MongoDB) -> Optional[List[str]]:
    try:
        automation_config = resource.get_om_tester().get_automation_config_tester().automation_config
    except Exception as e:
        print(f"Ops Manager automation config not available yet: {e}")
        return None
    return [process["name"] for process in automation_config.get("processes", [])]
