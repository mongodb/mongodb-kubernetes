import pymongo
import pytest
from kubernetes import client
from kubetester import pod_is_ready, try_load, wait_until
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.constants import OPERATOR_NAME

STATIC_GA_ARCHITECTURE = "static-ga"
NON_STATIC_ARCHITECTURE = "non-static"
REPLICA_SET_NAME = "mdb"
AGENT_CONTAINER_NAME = "mongodb-agent"
CUSTOM_AGENT_URL_ENV = "MDB_CUSTOM_AGENT_URL"

# Dev OM build that supports desiredInfraRevision; replace with a released version once OM ships the field.
DEV_OM_VERSION = "10.0.0-alpha0.489.20261106T1228Z"


@fixture(scope="module")
def ops_manager(namespace: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    try_load(resource)
    resource.set_version(DEV_OM_VERSION)
    resource.set_appdb_version(custom_appdb_version)
    # Non-static AppDB uses the operator's AGENT_IMAGE instead of an OM version -> agent version mapping.
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = NON_STATIC_ARCHITECTURE
    return resource


@fixture(scope="module", autouse=True)
def custom_agent_url_configured(namespace: str):
    deployment = client.AppsV1Api().read_namespaced_deployment(OPERATOR_NAME, namespace)
    env = deployment.spec.template.spec.containers[0].env or []
    assert any(var.name == CUSTOM_AGENT_URL_ENV and var.value for var in env), (
        f"{OPERATOR_NAME} deployment has no {CUSTOM_AGENT_URL_ENV}: pass --param mdb_custom_agent_url=<agent tarball URL> "
        "after building its mongodb-agent image with the manual_release_specific_agent patch variant"
    )


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=REPLICA_SET_NAME,
    ).configure(ops_manager, REPLICA_SET_NAME)
    resource.set_version(custom_mdb_version)

    try_load(resource)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = STATIC_GA_ARCHITECTURE
    return resource


@mark.e2e_om_static_ga_infra_revision
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_static_ga_infra_revision
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set.update()

    def reports_execution_step():
        try:
            automation_status = replica_set.get_om_tester().api_get_automation_status()
        except Exception as e:
            return False, f"automationStatus is not available: {e}"
        steps = {
            process.get("name"): process["execution"]["step"]
            for process in automation_status.get("processes", [])
            if isinstance(process.get("execution"), dict) and "step" in process["execution"]
        }
        return bool(steps), f"reported steps: {steps}"

    try:
        wait_until(
            reports_execution_step,
            timeout=600,
            sleep_time=5,
            msg="Ops Manager to report an execution step in automationStatus",
        )
    except AssertionError:
        pytest.fail(
            "Ops Manager never reported `execution` in automationStatus during the deployment; "
            "the OM build lacks the execution API (KUBE-549)"
        )

    replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)


@mark.e2e_om_static_ga_infra_revision
def test_statefulset_uses_on_delete_update_strategy(replica_set: MongoDB):
    update_strategy = replica_set.read_statefulset().spec.update_strategy
    # OnDelete keeps Kubernetes from rolling pods itself; only the operator's restart coordinator replaces them.
    assert update_strategy and update_strategy.type == "OnDelete", (
        f"StatefulSet {REPLICA_SET_NAME} update strategy is {update_strategy and update_strategy.type!r}, "
        "static-ga members must be replaced one at a time by the operator"
    )


def assert_infra_revisions_match(replica_set: MongoDB, namespace: str) -> str:
    """Asserts that each member's INFRA_REVISION, the automation config desiredInfraRevision and the StatefulSet
    status.updateRevision agree, and returns that revision."""
    update_revision = replica_set.read_statefulset().status.update_revision
    assert update_revision, f"StatefulSet {REPLICA_SET_NAME} has no status.updateRevision"

    processes = replica_set.get_automation_config_tester().automation_config["processes"]

    for idx in range(replica_set.get_members()):
        pod_name = f"{REPLICA_SET_NAME}-{idx}"

        infra_revision = KubernetesTester.run_command_in_pod_container(
            pod_name, namespace, ["printenv", "INFRA_REVISION"], container=AGENT_CONTAINER_NAME
        ).strip()
        assert infra_revision, f"INFRA_REVISION is not set on the mongodb-agent container of pod {pod_name}"

        matching = [p for p in processes if p["name"] in (pod_name, f"k8s/{namespace}/{pod_name}")]
        assert len(matching) == 1, (
            f"expected exactly one automation config process for pod {pod_name}, "
            f"got {[p['name'] for p in matching]}"
        )
        desired_infra_revision = matching[0].get("desiredInfraRevision")
        assert desired_infra_revision, f"automation config process for pod {pod_name} has no desiredInfraRevision"

        assert infra_revision == update_revision, (
            f"pod {pod_name} INFRA_REVISION {infra_revision!r} does not match "
            f"StatefulSet status.updateRevision {update_revision!r}"
        )
        assert desired_infra_revision == update_revision, (
            f"automation config process {matching[0]['name']!r} desiredInfraRevision {desired_infra_revision!r} "
            f"does not match StatefulSet status.updateRevision {update_revision!r}"
        )

    return update_revision


def read_replica_set_status(replica_set: MongoDB) -> dict:
    """Returns replSetGetStatus as reported by the current primary."""
    return replica_set.tester().client.admin.command("replSetGetStatus", read_preference=pymongo.ReadPreference.PRIMARY)


def primary_pod_name(status: dict) -> str:
    """Returns the pod of the member currently reported as PRIMARY."""
    member = next(m for m in status["members"] if m["stateStr"] == "PRIMARY")
    # member["name"] is the mongod host, e.g. mdb-1.mdb-svc.<namespace>.svc.cluster.local:27017.
    return member["name"].split(".")[0]


def read_member_pods(replica_set: MongoDB) -> dict:
    """Returns {pod_name: pod} for every member, mapping to None while the pod does not exist."""
    core_v1 = client.CoreV1Api()
    pods = {}
    for idx in range(replica_set.get_members()):
        pod_name = f"{REPLICA_SET_NAME}-{idx}"
        try:
            pods[pod_name] = core_v1.read_namespaced_pod(pod_name, replica_set.namespace)
        except client.ApiException as e:
            if e.status != 404:
                raise
            pods[pod_name] = None
    return pods


def count_unavailable_members(replica_set: MongoDB) -> int:
    """Counts members whose pod is missing, terminating or not Ready."""
    return sum(
        1
        for pod in read_member_pods(replica_set).values()
        if pod is None or pod.metadata.deletion_timestamp or not pod_is_ready(pod)
    )


@mark.e2e_om_static_ga_infra_revision
def test_infra_revision_matches_desired_infra_revision(replica_set: MongoDB, namespace: str):
    assert_infra_revisions_match(replica_set, namespace)


@mark.e2e_om_static_ga_infra_revision
def test_desired_infra_revision_follows_template_change(replica_set: MongoDB, namespace: str):
    """Rolls the members onto a new revision one at a time, primary last, with at most one election.

    Needs an agent implementing the WaitNodeReplacement replacement handshake. The operator takes the agent version
    from --param mdb_custom_agent_url=<agent tarball URL> and pulls the mongodb-agent image of that version, which
    the manual_release_specific_agent patch variant builds from the same tarball.
    """
    previous_revision = replica_set.read_statefulset().status.update_revision
    previous_uids = {pod_name: pod.metadata.uid for pod_name, pod in read_member_pods(replica_set).items()}
    status_before = read_replica_set_status(replica_set)
    previous_primary = primary_pod_name(status_before)
    previous_term = status_before["term"]

    # An agent container env var change is enough to produce a new StatefulSet revision.
    replica_set.load()
    replica_set["spec"].setdefault("agent", {}).setdefault("readinessProbe", {})["environmentVariables"] = {
        "LOG_FILE_PATH": "/var/log/mongodb-mms-automation/static-ga-readiness.log"
    }
    replica_set.update()

    # shortcut: 5 s sampling can miss a brief two-pod overlap; tighten once the operator exposes a rollout gauge.
    max_unavailable = 0

    # The rolling restart is done once pods, StatefulSet and automation config all agree on a new revision.
    def converged_on_new_revision():
        nonlocal max_unavailable
        try:
            max_unavailable = max(max_unavailable, count_unavailable_members(replica_set))
            revision = assert_infra_revisions_match(replica_set, namespace)
        except Exception as e:
            return False, str(e)
        return revision != previous_revision, f"revision is {revision}, previous was {previous_revision}"

    wait_until(
        converged_on_new_revision,
        timeout=1200,
        sleep_time=5,
        msg="infra revision to follow the template change",
    )
    assert max_unavailable <= 1, (
        f"{max_unavailable} members were unavailable at the same time during the rollout, "
        "the coordinator must replace one member at a time"
    )
    replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)

    revision = assert_infra_revisions_match(replica_set, namespace)
    assert revision != previous_revision, f"revision is still {revision!r}, the template change did not roll out"

    # A replacement always produces a new pod UID; only "differs" is observable from here.
    current_uids = {
        pod_name: pod.metadata.uid if pod else None for pod_name, pod in read_member_pods(replica_set).items()
    }
    unreplaced = [pod_name for pod_name, uid in previous_uids.items() if current_uids[pod_name] == uid]
    assert not unreplaced, f"member pods {unreplaced} were not replaced, uids {previous_uids} -> {current_uids}"

    status_after = read_replica_set_status(replica_set)
    term_delta = status_after["term"] - previous_term
    assert term_delta <= 1, (
        f"replica set term went from {previous_term} to {status_after['term']} during the rollout, "
        f"the coordinator must step the primary down at most once (primary was {previous_primary})"
    )

    creation_timestamps = {
        pod_name: pod.metadata.creation_timestamp for pod_name, pod in read_member_pods(replica_set).items()
    }
    members_created_after_primary = [
        pod_name
        for pod_name, created in creation_timestamps.items()
        if pod_name != previous_primary and created >= creation_timestamps[previous_primary]
    ]
    assert not members_created_after_primary, (
        f"primary pod {previous_primary} was not replaced last: {members_created_after_primary} were created at or "
        f"after it, creation timestamps {creation_timestamps}, primary term went from {previous_term} to "
        f"{status_after['term']}"
    )
