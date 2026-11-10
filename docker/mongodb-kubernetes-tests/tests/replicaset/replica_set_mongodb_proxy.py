"""Static-ga replica set with the suite's candidate Agent and proxy configuration."""

import json
import os
import re
import time
from typing import Optional

from kubernetes.client.rest import ApiException
from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_proxy import (
    AGENT_CONTAINER,
    DATABASE_CONTAINER,
    PROXY_ADDRESS,
    mongod_identity,
    post_to_proxy,
    proxy_events,
    proxy_image,
    proxy_log,
    query_proxy,
    query_proxy_path,
    read_processes,
    restart_counts,
    run_operator_in_cluster,
    wait_for_new_mongod,
    wait_until,
)
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_default_operator, local_operator


@fixture(scope="module")
def operator_with_proxy(namespace: str, operator_installation_config: dict[str, str]) -> Operator:
    config = dict(operator_installation_config)
    config["operator.mdbDefaultArchitecture"] = "static-ga"
    operator = get_default_operator(namespace, config)
    if local_operator():
        # A test run from a workstation cannot resolve the in-cluster name of the webhook, so it sets
        # LOCAL_OPERATOR=true, and Operator then installs the chart with 0 replicas. This test needs the operator in
        # the cluster, because the proxy image reaches it through its environment.
        run_operator_in_cluster(namespace)
    return operator


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    # The Ops Manager project keeps the authentication of an earlier run (also of one that failed half way), and a
    # new resource then starts with it. Asking for no authentication makes the operator switch it off.
    resource["spec"]["security"] = {"authentication": {"enabled": False}}
    # The proxy only exists in the static architecture, whatever the default of the test run is.
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static-ga"
    agent_image = os.environ.get("MDB_PROXY_TEST_AGENT_IMAGE")
    if agent_image:
        # Overriding the agent image makes the operator skip the agent version it would take from Ops Manager.
        resource["spec"]["podSpec"] = {
            "podTemplate": {"spec": {"containers": [{"name": AGENT_CONTAINER, "image": agent_image}]}}
        }
    try_load(resource)
    return resource


@mark.e2e_replica_set_mongodb_proxy
def test_install_operator(operator_with_proxy: Operator):
    # Installing the fixture already waited for the operator and its webhook.
    operator_with_proxy.wait_for_operator_ready()


@mark.e2e_replica_set_mongodb_proxy
def test_replica_set_reaches_running_phase(operator_with_proxy: Operator, replica_set: MongoDB):
    replica_set.update()
    # The agent starts mongod through the proxy, so a running replica set means the whole path works.
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_replica_set_mongodb_proxy
def test_pod_runs_the_proxy_in_the_database_container(replica_set: MongoDB, namespace: str):
    sts = KubernetesTester.clients("appsv1").read_namespaced_stateful_set(replica_set.name, namespace)
    pod_spec = sts.spec.template.spec

    assert not pod_spec.share_process_namespace

    containers = {c.name: c for c in pod_spec.containers}
    assert containers[DATABASE_CONTAINER].command == ["/mnt/proxy/proxy"]

    volumes = {v.name: v for v in pod_spec.volumes}
    assert volumes["mongodb-proxy"].image.reference == proxy_image()
    assert volumes["mongodb-binaries"].image is not None

    env = {e.name: e.value for e in containers[AGENT_CONTAINER].env}
    assert env["MDB_MONGODB_PROXY_ADDRESS"] == PROXY_ADDRESS


@mark.e2e_replica_set_mongodb_proxy
def test_agent_was_started_with_the_proxy_flag(replica_set: MongoDB, namespace: str):
    # The command line holds the API key, so only the names of the matching processes are read. The bracket in the
    # pattern keeps grep and its parent shell, whose command lines contain the pattern itself, from matching.
    out = KubernetesTester.run_command_in_pod_container(
        f"{replica_set.name}-0",
        namespace,
        [
            "bash",
            "-c",
            "grep -la -e '-mongodProxyAddress=127.0.0.1:80[9]9' /proc/[0-9]*/cmdline 2>/dev/null | head -n 1",
        ],
        container=AGENT_CONTAINER,
    )

    assert out.strip(), "no process of the agent container runs with -mongodProxyAddress=" + PROXY_ADDRESS


@mark.e2e_replica_set_mongodb_proxy
def test_proxy_reports_the_running_database(replica_set: MongoDB, namespace: str):
    for i in range(replica_set["spec"]["members"]):
        status = query_proxy(f"{replica_set.name}-{i}", namespace)

        assert status["state"] == "running", status
        assert status["role"] == "main", status


@mark.e2e_replica_set_mongodb_proxy
def test_proxy_reports_events_and_timestamps(replica_set: MongoDB, namespace: str):
    pod = f"{replica_set.name}-0"
    status = query_proxy(pod, namespace)
    assert status["state"] == "running", status
    assert status.get("startedAt"), status
    assert status.get("uptimeSeconds", 0) > 0, status

    body = query_proxy_path(pod, namespace, "/v1/events")
    assert body["capacity"] == 256, body
    assert body["dropped"] == 0, body
    names = [event["action"] for event in body["events"]]
    assert "child_started" in names, body
    child_started = next(event for event in body["events"] if event["action"] == "child_started")
    assert child_started["role"] == "main", child_started
    assert child_started["pid"] > 0, child_started

    start_events = [event for event in body["events"] if event["action"] == "start"]
    assert start_events, body
    start_request = start_events[0]
    assert start_request["request"] == "POST /v1/process/start", start_request
    assert start_request["outcome"] == "success", start_request
    assert start_request["returned"] == "200", start_request
    assert start_request["requestId"], start_request


@mark.e2e_replica_set_mongodb_proxy
def test_mongod_runs_in_its_own_container(replica_set: MongoDB, namespace: str):
    for i in range(replica_set["spec"]["members"]):
        pod = f"{replica_set.name}-{i}"

        in_agent_container = read_processes(pod, namespace, AGENT_CONTAINER)
        in_database_container = read_processes(pod, namespace, DATABASE_CONTAINER)

        assert "mongod" not in in_agent_container
        assert "mongod" in in_database_container
        assert "proxy" in in_database_container


@mark.e2e_replica_set_mongodb_proxy
def test_log_of_mongod_is_in_the_log_of_the_database_container(replica_set: MongoDB, namespace: str):
    """`kubectl logs` of the database container has the log of mongod, not only the lines of the proxy.

    mongod writes its log to a file on the logs volume, which the agent and Ops Manager read. The proxy follows the
    file and copies each line to its stdout.
    """
    corev1 = KubernetesTester.clients("corev1")
    for pod in pod_members(replica_set):

        def has_mongod_log(pod=pod) -> bool:
            raw = corev1.read_namespaced_pod_log(pod, namespace, container=DATABASE_CONTAINER)
            # a structured mongod log line, which the log of the proxy has no "c" (component) field for
            return any(
                '"msg":"Waiting for connections"' in line and '"c":"NETWORK"' in line for line in raw.splitlines()
            )

        wait_until(f"the log of mongod in the log of {pod}", has_mongod_log, timeout_seconds=120)


def wait_for_replaced_pod(pod: str, namespace: str, old_uid: str, timeout_seconds: int = 600):
    """Waits until the pod has been recreated (another uid) and all its containers are ready."""
    corev1 = KubernetesTester.clients("corev1")
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        try:
            current = corev1.read_namespaced_pod(pod, namespace)
        except ApiException as e:
            if e.status != 404:
                raise
            current = None
        if current is not None and current.metadata.uid != old_uid:
            statuses = current.status.container_statuses or []
            if statuses and all(c.ready for c in statuses):
                return
        time.sleep(3)
    raise Exception(f"{pod} was not recreated and ready in {timeout_seconds}s")


@mark.e2e_replica_set_mongodb_proxy
def test_replica_set_is_still_running_after_a_pod_restart(replica_set: MongoDB, namespace: str):
    """Deleting a pod exercises the stop path: SIGTERM reaches the proxy and the proxy stops mongod."""
    pod = f"{replica_set.name}-2"
    corev1 = KubernetesTester.clients("corev1")
    old_uid = corev1.read_namespaced_pod(pod, namespace).metadata.uid

    corev1.delete_namespaced_pod(pod, namespace)
    wait_for_replaced_pod(pod, namespace, old_uid)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    # The new pod has a new proxy, which is idle until the agent has started mongod again.
    deadline = time.time() + 300
    status = query_proxy(pod, namespace)
    while status["state"] != "running" and time.time() < deadline:
        time.sleep(3)
        status = query_proxy(pod, namespace)
    assert status["state"] == "running", status


def restart_agent_container(pod: str, namespace: str):
    before = restart_counts(pod, namespace)[AGENT_CONTAINER]
    KubernetesTester.run_command_in_pod_container(
        pod, namespace, ["bash", "-c", "kill -TERM 1"], container=AGENT_CONTAINER
    )

    def restarted_and_ready() -> bool:
        current = KubernetesTester.clients("corev1").read_namespaced_pod(pod, namespace)
        return any(
            c.name == AGENT_CONTAINER and c.restart_count > before and c.ready
            for c in current.status.container_statuses
        )

    wait_until("the agent container to restart and become ready", restarted_and_ready, timeout_seconds=180)


def pod_members(replica_set: MongoDB) -> list[str]:
    return [f"{replica_set.name}-{i}" for i in range(replica_set["spec"]["members"])]


def pod_uids(replica_set: MongoDB, namespace: str) -> dict[str, str]:
    corev1 = KubernetesTester.clients("corev1")
    return {pod: corev1.read_namespaced_pod(pod, namespace).metadata.uid for pod in pod_members(replica_set)}


@mark.e2e_replica_set_mongodb_proxy
def test_agent_restarts_mongod_through_the_proxy_on_a_config_change(replica_set: MongoDB, namespace: str):
    """A changed mongod option is an automation config change, not a pod template change.

    The pods stay as they are, so a new mongod can only come from the agent: the driver `shutdown` command, the
    process-down check against the proxy and a new `start` request.
    """
    pods = pod_members(replica_set)
    identities = {pod: mongod_identity(pod, namespace) for pod in pods}
    uids = pod_uids(replica_set, namespace)
    restarts = {pod: restart_counts(pod, namespace) for pod in pods}
    assert all(identities.values()), identities

    # A different value than the one mongod runs with. The resource does not tell it: the processes of an earlier run
    # can stay in the Ops Manager project, and the operator merges a new process into the existing one.
    running = KubernetesTester.run_command_in_pod_container(
        pods[0], namespace, ["cat", "/data/automation-mongod.conf"], container=AGENT_CONTAINER
    )
    match = re.search(r"maxIncomingConnections: (\d+)", running)
    new_limit = int(match.group(1)) + 1 if match else 1234
    replica_set["spec"]["additionalMongodConfig"] = {"net": {"maxIncomingConnections": new_limit}}
    replica_set.update()

    for pod in pods:
        wait_for_new_mongod(pod, namespace, identities[pod], timeout_seconds=900)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    assert pod_uids(replica_set, namespace) == uids, "a pod was replaced, so the agent did not restart mongod"
    assert {pod: restart_counts(pod, namespace) for pod in pods} == restarts, "a container was restarted"

    for pod in pods:
        config = KubernetesTester.run_command_in_pod_container(
            pod, namespace, ["cat", "/data/automation-mongod.conf"], container=AGENT_CONTAINER
        )
        assert f"maxIncomingConnections: {new_limit}" in config, config
        assert "fork" not in config, config

    # The proxy log tells how the old mongod went down: it exited by itself (the driver `shutdown` command) and the
    # proxy never had to kill it.
    for pod in pods:
        exits = proxy_events(pod, namespace, "process exited")
        assert exits, f"{pod}: the proxy did not log the exit of the old mongod"
        assert exits[0]["exitCode"] == 0 and not exits[0].get("signal"), exits[0]
        assert len(proxy_events(pod, namespace, "process started")) >= 2, pod
        assert not proxy_events(pod, namespace, "sending SIGKILL"), pod

        status = query_proxy(pod, namespace)
        last_exit = status.get("lastExit")
        assert last_exit is not None, status
        assert last_exit["exitCode"] == 0 and not last_exit.get("signal"), last_exit
        assert last_exit.get("exitedAt"), last_exit


@mark.e2e_replica_set_mongodb_proxy
def test_agent_starts_mongod_again_after_it_was_killed(replica_set: MongoDB, namespace: str):
    """A crash of mongod leaves the proxy alive with an exit status, and the agent's replan loop starts it again."""
    pod = f"{replica_set.name}-1"
    identity = mongod_identity(pod, namespace)
    restarts = restart_counts(pod, namespace)
    assert identity

    KubernetesTester.run_command_in_pod_container(
        pod, namespace, ["bash", "-c", f"kill -9 {identity.split(':')[0]}"], container=DATABASE_CONTAINER
    )

    wait_for_new_mongod(pod, namespace, identity, timeout_seconds=600)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)
    assert restart_counts(pod, namespace) == restarts, "the container was restarted, the proxy must have survived"

    # The proxy saw the crash and the agent's new start request.
    assert any(e.get("signal") == "killed" for e in proxy_events(pod, namespace, "process exited"))
    last_exit = query_proxy(pod, namespace).get("lastExit")
    assert last_exit is not None and last_exit.get("signal") == "killed", last_exit


@mark.e2e_replica_set_mongodb_proxy
def test_proxy_stop_request_kills_a_mongod_that_does_not_exit(replica_set: MongoDB, namespace: str):
    """A mongod that does not leave on SIGTERM is left alone by a stop, and killed by a stop with force.

    The stop endpoint is what the agent calls when the shutdown command times out (StopProcess with force). No normal
    planner move gets there on an MCK deployment, so the test makes the request itself, with the same fields as the
    agent: role, operationId of the running process, a timeout and force. SIGSTOP makes mongod ignore SIGTERM (the
    signal stays pending), which is a mongod that does not stop. The proxy must not kill it without force, must kill it
    with force, and the agent must then start mongod again.
    """
    pod = f"{replica_set.name}-2"
    identity = mongod_identity(pod, namespace)
    restarts = restart_counts(pod, namespace)
    assert identity
    status = query_proxy(pod, namespace)
    assert status["state"] == "running"
    request = {"timeoutSeconds": 2, "role": status["role"]}
    if status.get("operationId"):
        request["operationId"] = status["operationId"]
    kills_before = len(proxy_events(pod, namespace, "sending SIGKILL"))

    KubernetesTester.run_command_in_pod_container(
        pod, namespace, ["bash", "-c", f"kill -STOP {identity.split(':')[0]}"], container=DATABASE_CONTAINER
    )

    # Without force the proxy sends SIGTERM, waits for the timeout and leaves the process alone.
    code, body = post_to_proxy(pod, namespace, "/v1/process/stop", request)
    assert code == 504, (code, body)
    assert body.get("code") == "stop_timeout" or "stop_timeout" in json.dumps(body), body
    assert mongod_identity(pod, namespace) == identity, "a stop without force must not kill the process"
    assert query_proxy(pod, namespace)["state"] == "running"
    assert len(proxy_events(pod, namespace, "sending SIGKILL")) == kills_before
    assert proxy_events(pod, namespace, "sending SIGTERM"), "the proxy must have asked the process to stop"

    # With force it sends SIGKILL after the timeout and reports the process stopped.
    code, body = post_to_proxy(pod, namespace, "/v1/process/stop", {**request, "force": True})
    assert code == 200, (code, body)
    assert body["state"] == "exited", body
    assert body.get("signal") == "killed", body
    assert len(proxy_events(pod, namespace, "sending SIGKILL")) == kills_before + 1

    # The agent notices that mongod is down and starts it again, in the same container.
    wait_for_new_mongod(pod, namespace, identity, timeout_seconds=600)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)
    assert restart_counts(pod, namespace) == restarts, "the container was restarted, the proxy must have survived"


@mark.e2e_replica_set_mongodb_proxy
def test_agent_starts_mongod_again_after_the_database_container_restarted(replica_set: MongoDB, namespace: str):
    """A restarted proxy is idle and starts nothing by itself, so the agent has to notice and start mongod."""
    pod = f"{replica_set.name}-1"
    identity = mongod_identity(pod, namespace)
    before = restart_counts(pod, namespace)[DATABASE_CONTAINER]
    assert identity

    # SIGTERM to the proxy, PID 1: it stops mongod and exits, and the kubelet restarts the container.
    KubernetesTester.run_command_in_pod_container(
        pod, namespace, ["bash", "-c", "kill -TERM 1"], container=DATABASE_CONTAINER
    )

    wait_until(
        "the database container to restart",
        lambda: restart_counts(pod, namespace)[DATABASE_CONTAINER] > before,
        timeout_seconds=120,
    )
    wait_for_new_mongod(pod, namespace, identity, timeout_seconds=600)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_replica_set_mongodb_proxy
def test_agent_container_restart_does_not_restart_mongod(replica_set: MongoDB, namespace: str):
    """The proxy and mongod keep running while the agent restarts, and the agent must not restart mongod for it."""
    pod = f"{replica_set.name}-0"
    identity = mongod_identity(pod, namespace)
    assert identity

    restart_agent_container(pod, namespace)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)
    # Give the restarted agent time to plan: it must find mongod running and leave it alone.
    time.sleep(60)
    assert mongod_identity(pod, namespace) == identity
    assert query_proxy(pod, namespace)["state"] == "running"


MEMBER_CONFIG = "/data/automation-mongod.conf"


@mark.e2e_replica_set_mongodb_proxy
def test_agent_stops_a_leftover_ephemeral_mongod_when_it_starts(replica_set: MongoDB, namespace: str):
    """An ephemeral mongod that the proxy still runs when the agent starts is stopped, then the member starts again.

    The agent cannot see the pid of a process in the other container, so at start it asks the proxy for the role of its
    process (StopLeftoverEphemeralViaProxy). The agent starts its ephemeral mongod with the config file of the member
    (observed with a real index build: same configPath, only the role differs), so the test does the same: the member
    leaves, and the proxy runs the config of the member with the role ephemeral, as an agent that died during a
    maintenance run would leave it. Then the agent container restarts.
    """
    pod = f"{replica_set.name}-2"
    identity = mongod_identity(pod, namespace)
    restarts = restart_counts(pod, namespace)
    assert identity
    # the proxy runs one process: the member leaves, the ephemeral one takes its place
    status, body = post_to_proxy(pod, namespace, "/v1/process/stop", {"timeoutSeconds": 60, "role": "main"})
    assert status == 200 and body["state"] == "exited", (status, body)
    status, body = post_to_proxy(
        pod, namespace, "/v1/process/start", {"configPath": MEMBER_CONFIG, "role": "ephemeral"}
    )
    assert status == 200 and body["role"] == "ephemeral" and body["state"] == "running", (status, body)
    ephemeral_identity = mongod_identity(pod, namespace)
    assert ephemeral_identity and ephemeral_identity != identity

    KubernetesTester.run_command_in_pod_container(
        pod, namespace, ["bash", "-c", "kill -TERM 1"], container=AGENT_CONTAINER
    )
    wait_until(
        "the agent container to restart",
        lambda: restart_counts(pod, namespace)[AGENT_CONTAINER] > restarts[AGENT_CONTAINER],
        timeout_seconds=180,
    )

    def member_runs_again() -> bool:
        current = mongod_identity(pod, namespace)
        state = query_proxy(pod, namespace)
        return (
            current not in ("", identity, ephemeral_identity)
            and state["role"] == "main"
            and state["state"] == "running"
        )

    # the ephemeral mongod is a new process too, so only the role of the process in the proxy tells the member
    wait_until(f"{pod} to run the member again", member_runs_again, 600)
    stops = [e for e in proxy_events(pod, namespace, "stop requested") if e.get("role") == "ephemeral"]
    assert stops and all(e["http_status"] == 200 for e in stops), stops
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)
    assert restart_counts(pod, namespace)[DATABASE_CONTAINER] == restarts[DATABASE_CONTAINER]


INDEX_DATABASE = "proxytest"
INDEX_COLLECTION = "items"
INDEX_NAME = "field_1"


def mongosh(pod: str, namespace: str, script: str, uri: str) -> str:
    """Runs a script with mongosh in the database container (it has the shell) and returns the last line."""
    # The home directory of the user of the container is not writable, and mongosh warns about it on stdout.
    out = KubernetesTester.run_command_in_pod_container(
        pod, namespace, ["env", "HOME=/tmp", "mongosh", "--quiet", uri, "--eval", script], container=DATABASE_CONTAINER
    ).strip()
    return out.splitlines()[-1] if out else ""


def replica_set_uri(replica_set: MongoDB, namespace: str) -> str:
    host = f"{replica_set.name}-0.{replica_set.name}-svc.{namespace}.svc.cluster.local:27017"
    return f"mongodb://{host}/?replicaSet={replica_set.name}"


def ephemeral_starts(pod: str, namespace: str) -> list[dict]:
    return [e for e in proxy_events(pod, namespace, "start requested") if e.get("role") == "ephemeral"]


def fresh_successful_ephemeral_starts(events: list[dict], previous_events: list[dict]) -> list[dict]:
    previous_requests = {event.get("request_id") for event in previous_events}
    identity_pattern = r"[A-Za-z0-9_.-]{1,128}"
    fresh = []
    for event in events:
        if not (
            event.get("role") == "ephemeral"
            and event.get("msg") == "start requested"
            and isinstance(event.get("request_id"), str)
            and re.fullmatch(identity_pattern, event["request_id"])
            and isinstance(event.get("operation_id"), str)
            and (event["operation_id"] == "" or re.fullmatch(identity_pattern, event["operation_id"]))
            and event["request_id"] not in previous_requests
            and event.get("http_status") == 200
            and event.get("error_class") == ""
        ):
            continue
        fresh.append(event)
    return fresh


@mark.e2e_replica_set_mongodb_proxy
def test_agent_runs_an_ephemeral_mongod_to_build_an_index(replica_set: MongoDB, namespace: str):
    """A rolling index build takes each secondary out of the replica set and builds the index on a standalone mongod.

    Ops Manager starts it with `indexConfigs` in the automation config. For a replica set of three or more members
    the agent starts the secondary as a standalone on an ephemeral port, builds the index, stops it and starts the
    member again: the proxy gets a `start` request with the role `ephemeral`.
    """
    pods = pod_members(replica_set)
    uri = replica_set_uri(replica_set, namespace)
    assert len(pods) >= 3, "the agent builds the index in place on a replica set of fewer than three members"

    mongosh(
        pods[0],
        namespace,
        f'db.getSiblingDB("{INDEX_DATABASE}").{INDEX_COLLECTION}.insertMany('
        f"Array.from({{length: 100}}, (_, i) => ({{_id: i, field: i}})))",
        uri,
    )

    om = replica_set.get_om_tester()
    automation_config = om.api_get_automation_config()
    automation_config["indexConfigs"] = [
        {
            "key": [["field", 1]],
            "rsName": replica_set.name,
            "dbName": INDEX_DATABASE,
            "collectionName": INDEX_COLLECTION,
            "options": {"name": INDEX_NAME},
        }
    ]
    previous_starts = {pod: ephemeral_starts(pod, namespace) for pod in pods}
    om.api_put_automation_config(automation_config)

    def built_on_all_members() -> bool:
        script = (
            f'db.getSiblingDB("{INDEX_DATABASE}").{INDEX_COLLECTION}.getIndexes()'
            f'.filter(i => i.name === "{INDEX_NAME}").length'
        )
        for pod in pods:
            # read from the member itself, which also reads on a secondary
            direct = f"mongodb://localhost:27017/?directConnection=true&readPreference=secondaryPreferred"
            if mongosh(pod, namespace, script, direct) != "1":
                return False
        return True

    wait_until("the index on every member", built_on_all_members, timeout_seconds=900, interval_seconds=10)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    # Ops Manager changes the automation config itself while it works through the index builds, and an operator that
    # publishes at the same time is refused (AUTOMATION_CONFIG_CONCURRENT_MODIFICATION), so the entry goes away now.
    automation_config = om.api_get_automation_config()
    automation_config["indexConfigs"] = []
    om.api_put_automation_config(automation_config)

    started = {
        pod: fresh_successful_ephemeral_starts(ephemeral_starts(pod, namespace), previous_starts[pod]) for pod in pods
    }
    assert any(started.values()), f"No new successful ephemeral start request in proxy logs: {started}"
    for pod in pods:
        # `fresh_successful_ephemeral_starts` already requires http_status 200: the proxy log of a start has no
        # config path, only whether the request was accepted.
        assert query_proxy(pod, namespace)["role"] == "main", pod


SCRAM_USER = "proxy-user"
SCRAM_PASSWORD = "proxy-password-1"


def member_uri(user: Optional[str] = None, password: Optional[str] = None) -> str:
    """A connection string to the mongod of the pod itself, which a secondary also answers."""
    credentials = f"{user}:{password}@" if user else ""
    return f"mongodb://{credentials}localhost:27017/?directConnection=true&readPreference=secondaryPreferred&authSource=admin"


def unauthenticated_result(pod: str, namespace: str) -> str:
    script = 'try { db.adminCommand({listDatabases: 1}); print("allowed") } catch (e) { print("denied:" + e.codeName) }'
    return mongosh(pod, namespace, script, member_uri())


@mark.e2e_replica_set_mongodb_proxy
def test_agent_starts_mongod_with_scram_authentication(replica_set: MongoDB, namespace: str):
    """Authentication adds a keyfile and the credentials of the agent, and restarts every member.

    The agent writes the keyfile in its container and mongod, in the other container, reads it.
    """
    replica_set["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
    replica_set.update()

    # A publish that Ops Manager refuses as concurrent leaves the resource Failed until the operator tries again.
    replica_set.assert_reaches_phase(Phase.Running, timeout=1500, ignore_errors=True)

    for pod in pod_members(replica_set):
        assert query_proxy(pod, namespace)["state"] == "running", pod
        assert unauthenticated_result(pod, namespace) == "denied:Unauthorized", pod


@mark.e2e_replica_set_mongodb_proxy
def test_user_can_read_the_data_with_scram(replica_set: MongoDB, namespace: str):
    from kubetester.scram import build_scram_user_resource

    user = build_scram_user_resource(namespace, SCRAM_USER, SCRAM_PASSWORD, f"{SCRAM_USER}-password", replica_set.name)
    # the fixture grants roles on admin only
    user["spec"]["roles"] = [{"db": INDEX_DATABASE, "name": "read"}]
    user.update()
    user.assert_reaches_phase(Phase.Updated, timeout=600)

    script = f'db.getSiblingDB("{INDEX_DATABASE}").{INDEX_COLLECTION}.countDocuments({{}})'
    for pod in pod_members(replica_set):
        assert mongosh(pod, namespace, script, member_uri(SCRAM_USER, SCRAM_PASSWORD)) == "100", pod


@mark.e2e_replica_set_mongodb_proxy
def test_agent_starts_mongod_again_after_it_was_killed_with_authentication(replica_set: MongoDB, namespace: str):
    """The agent has to start mongod again with the keyfile and has to log in to it to see it is up."""
    pod = f"{replica_set.name}-1"
    identity = mongod_identity(pod, namespace)
    assert identity

    KubernetesTester.run_command_in_pod_container(
        pod, namespace, ["bash", "-c", f"kill -9 {identity.split(':')[0]}"], container=DATABASE_CONTAINER
    )

    wait_for_new_mongod(pod, namespace, identity, timeout_seconds=600)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)
    assert unauthenticated_result(pod, namespace) == "denied:Unauthorized"
    script = f'db.getSiblingDB("{INDEX_DATABASE}").{INDEX_COLLECTION}.countDocuments({{}})'
    assert mongosh(pod, namespace, script, member_uri(SCRAM_USER, SCRAM_PASSWORD)) == "100"


@mark.e2e_replica_set_mongodb_proxy
def test_agent_starts_mongod_without_authentication_when_it_is_switched_off(replica_set: MongoDB, namespace: str):
    """Switching authentication off restarts the members once more, and leaves the Ops Manager project as it was.

    The project keeps its authentication settings after the resource is deleted, and every resource that is created
    in it next would start with authentication, so a test that enables it also has to switch it off.
    """
    from kubetester.mongodb_user import MongoDBUser

    user = MongoDBUser.from_yaml(yaml_fixture("scram-sha-user.yaml"), namespace=namespace, name=SCRAM_USER)
    user.delete()

    replica_set["spec"]["security"] = {"authentication": {"enabled": False}}
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=1500, ignore_errors=True)
    for pod in pod_members(replica_set):
        assert query_proxy(pod, namespace)["state"] == "running", pod
        assert unauthenticated_result(pod, namespace) == "allowed", pod
