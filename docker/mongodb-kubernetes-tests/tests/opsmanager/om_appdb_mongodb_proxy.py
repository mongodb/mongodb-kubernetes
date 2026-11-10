"""
A replica set that runs mongod through the mongodb-proxy and is managed by an Ops Manager of the operator
(MongoDBOpsManager), not by Cloud Manager. The agents then talk to an in-cluster Ops Manager with its own AppDB.

Ops Manager is heavy: its image only exists for amd64 (under emulation on an arm64 workstation it needs about 12
minutes and 4 GB), so the test is kept apart from replicaset/replica_set_mongodb_proxy.py, which runs against
Cloud Manager.

The prerequisites are those of replicaset/replica_set_mongodb_proxy.py (Kubernetes 1.36 or newer,
MDB_MONGODB_PROXY_IMAGE, MDB_PROXY_TEST_AGENT_IMAGE); the test is skipped when MDB_MONGODB_PROXY_IMAGE is not set.

Not covered yet: the AppDB behind the proxy. The AppDB statefulset has no proxy support in this branch, so the
AppDB keeps running mongod in the database container.
"""

import os
from typing import Optional

from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_proxy import (
    AGENT_CONTAINER,
    DATABASE_CONTAINER,
    PROXY_ADDRESS,
    PROXY_ANNOTATION,
    mongod_identity,
    proxy_events,
    proxy_helm_values,
    proxy_image,
    query_proxy,
    restart_counts,
    run_operator_in_cluster,
    wait_for_new_mongod,
)
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark, skip
from tests.conftest import get_default_operator, local_operator

OM_RESOURCE_NAME = "om-proxy"
REPLICA_SET_NAME = "rs-proxy"


@fixture(scope="module")
def default_operator(namespace: str, operator_installation_config: dict[str, str]) -> Operator:
    """The operator with the proxy image. The tests of this directory install `default_operator` before every test,
    so redefining it here makes the proxy-aware installation the only one."""
    image = proxy_image()
    if image is None:
        skip("MDB_MONGODB_PROXY_IMAGE is not set")
    operator = get_default_operator(namespace, {**operator_installation_config, **proxy_helm_values(image)})
    if local_operator():
        # The proxy image reaches the operator through its environment, so the operator has to run in the cluster.
        run_operator_in_cluster(namespace)
    return operator


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"),
        name=OM_RESOURCE_NAME,
        namespace=namespace,
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    # The replica set below is static, and so is the architecture the AppDB follows from the resource.
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"

    try_load(resource)
    return resource


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set.yaml"), name=REPLICA_SET_NAME, namespace=namespace
    ).configure(ops_manager, REPLICA_SET_NAME)
    resource.set_version(custom_mdb_version)
    resource["metadata"].setdefault("annotations", {}).update(
        {
            "mongodb.com/v1.architecture": "static",
            PROXY_ANNOTATION: "true",
        }
    )
    agent_image = os.environ.get("MDB_PROXY_TEST_AGENT_IMAGE")
    if agent_image:
        resource["spec"]["podSpec"] = {
            "podTemplate": {"spec": {"containers": [{"name": AGENT_CONTAINER, "image": agent_image}]}}
        }
    try_load(resource)
    return resource


@mark.e2e_om_appdb_mongodb_proxy
def test_create_ops_manager(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    # The first start takes long, in particular on an emulated platform.
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_appdb_mongodb_proxy
def test_replica_set_reaches_running_phase_under_ops_manager(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_appdb_mongodb_proxy
def test_replica_set_pods_run_the_proxy(replica_set: MongoDB, namespace: str):
    sts = KubernetesTester.clients("appsv1").read_namespaced_stateful_set(replica_set.name, namespace)
    containers = {c.name: c for c in sts.spec.template.spec.containers}
    assert containers[DATABASE_CONTAINER].command == ["/mnt/proxy/proxy"]
    env = {e.name: e.value for e in containers[AGENT_CONTAINER].env}
    assert env["MDB_MONGODB_PROXY_ADDRESS"] == PROXY_ADDRESS
    assert not sts.spec.template.spec.share_process_namespace

    for member in range(replica_set["spec"]["members"]):
        status = query_proxy(f"{replica_set.name}-{member}", namespace)
        assert status["state"] == "running", status
        assert status["role"] == "main", status


@mark.e2e_om_appdb_mongodb_proxy
def test_replica_set_is_managed_by_the_ops_manager_of_the_operator(ops_manager: MongoDBOpsManager, replica_set: MongoDB):
    # The operator reports Running only after the agents reached the goal state in Ops Manager, and the link in the
    # status leads to the project there. The Ops Manager API itself is not called: the test runs outside the cluster
    # and cannot resolve the service name of Ops Manager.
    replica_set.load()
    link = replica_set["status"]["link"]
    assert link.startswith(ops_manager.om_status().get_url()), link
    assert "/v2/" in link, link


@mark.e2e_om_appdb_mongodb_proxy
def test_agent_starts_mongod_again_after_it_was_killed_under_ops_manager(replica_set: MongoDB, namespace: str):
    pod = f"{replica_set.name}-1"
    identity = mongod_identity(pod, namespace)
    restarts = restart_counts(pod, namespace)
    assert identity

    KubernetesTester.run_command_in_pod_container(
        pod, namespace, ["bash", "-c", f"kill -9 {identity.split(':')[0]}"], container=DATABASE_CONTAINER
    )

    wait_for_new_mongod(pod, namespace, identity, timeout_seconds=600)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)
    assert restart_counts(pod, namespace) == restarts
    assert any(e.get("signal") == "killed" for e in proxy_events(pod, namespace, "process exited"))
