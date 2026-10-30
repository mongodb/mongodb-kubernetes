"""
A replica set whose members run mongod through the mongodb-proxy in a container of its own.

Prerequisites, none of which the default e2e environment provides:
  * Kubernetes 1.36 or newer (the proxy and the MongoDB binaries are mounted as OCI image volumes).
  * MDB_MONGODB_PROXY_IMAGE: the full reference of a mongodb-proxy image the cluster can pull,
    for example the result of `make mongodb-proxy-image` pushed to a registry.
  * An agent image whose /agent/mongodb-agent supports --mongodProxyAddress. Until a release has it, build one:
    `task build` in mms-automation (GOOS/GOARCH of the cluster nodes), copy the binary over /agent/mongodb-agent
    of the released agent image, push the result to a registry the cluster can pull from and point the operator
    at it (registry.agent, agent.name, agent.version). MDB_CUSTOM_AGENT_URL does not help: the launcher only
    downloads an agent in the non-static architecture.

The test is skipped when MDB_MONGODB_PROXY_IMAGE is not set.
"""

import json
import os
from typing import Optional

from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark, skip
from tests.conftest import get_default_operator

PROXY_ANNOTATION = "mongodb.com/v1.mongodbProxy"
PROXY_ADDRESS = "127.0.0.1:8099"
AGENT_CONTAINER = "mongodb-agent"
DATABASE_CONTAINER = "mongodb-enterprise-database"


def proxy_image() -> Optional[str]:
    return os.environ.get("MDB_MONGODB_PROXY_IMAGE") or None


@fixture(scope="module")
def operator_with_proxy(namespace: str, operator_installation_config: dict[str, str]) -> Operator:
    image = proxy_image()
    if image is None:
        skip("MDB_MONGODB_PROXY_IMAGE is not set")
    config = dict(operator_installation_config)
    config["customEnvVars"] = config.get("customEnvVars", "") + f"\\&MDB_MONGODB_PROXY_IMAGE={image}"
    return get_default_operator(namespace, config)


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    # The proxy only exists in the static architecture, whatever the default of the test run is.
    resource["metadata"].setdefault("annotations", {}).update(
        {
            "mongodb.com/v1.architecture": "static",
            PROXY_ANNOTATION: "true",
        }
    )
    try_load(resource)
    return resource


def read_processes(pod: str, namespace: str, container: str) -> list[str]:
    """Returns the names of the processes visible in the container."""
    out = KubernetesTester.run_command_in_pod_container(
        pod,
        namespace,
        ["bash", "-c", 'for p in /proc/[0-9]*; do cat "$p/comm" 2>/dev/null; done'],
        container=container,
    )
    return [line.strip() for line in out.splitlines() if line.strip()]


def query_proxy(pod: str, namespace: str) -> dict:
    """Asks the proxy for its state from the agent container, the way the agent does it."""
    out = KubernetesTester.run_command_in_pod_container(
        pod,
        namespace,
        [
            "bash",
            "-c",
            "exec 3<>/dev/tcp/127.0.0.1/8099; "
            "printf 'GET /v1/process HTTP/1.0\\r\\nHost: proxy\\r\\n\\r\\n' >&3; cat <&3",
        ],
        container=AGENT_CONTAINER,
    )
    body = out.split("\r\n\r\n", 1)[-1]
    return json.loads(body)


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
    logs = KubernetesTester.read_pod_logs(namespace, f"{replica_set.name}-0", container=AGENT_CONTAINER)

    assert f"-mongodProxyAddress={PROXY_ADDRESS}" in logs


@mark.e2e_replica_set_mongodb_proxy
def test_proxy_reports_the_running_database(replica_set: MongoDB, namespace: str):
    for i in range(replica_set["spec"]["members"]):
        status = query_proxy(f"{replica_set.name}-{i}", namespace)

        assert status["state"] == "running", status
        assert status["role"] == "main", status


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
def test_replica_set_is_still_running_after_a_pod_restart(replica_set: MongoDB, namespace: str):
    """Deleting a pod exercises the stop path: SIGTERM reaches the proxy and the proxy stops mongod."""
    KubernetesTester.clients("corev1").delete_namespaced_pod(f"{replica_set.name}-2", namespace)

    replica_set.assert_reaches_phase(Phase.Running, timeout=900)
    assert query_proxy(f"{replica_set.name}-2", namespace)["state"] == "running"
