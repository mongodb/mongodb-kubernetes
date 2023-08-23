from typing import Optional, List

from kubernetes.client import V1Container
from pytest import mark, fixture

from kubetester import find_fixture, create_or_update
from kubetester.kubetester import ensure_nested_objects
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_appdb_multi_cluster_deployment

AGENT_NAME = "mongodb-agent"
MONGOD_NAME = "mongod"
MONITORING_AGENT_NAME = "mongodb-agent-monitoring"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        find_fixture("om_appdb_configure_all_images.yaml"),
        namespace=namespace,
        name="om-configure-all-appdb-images",
    )

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    ensure_nested_objects(resource, ["spec", "applicationDatabase", "podSpec", "podTemplate", "spec"])

    resource["spec"]["applicationDatabase"]["version"] = "4.4.0"

    resource["spec"]["applicationDatabase"]["podSpec"]["podTemplate"]["spec"]["containers"] = [
        {
            "name": AGENT_NAME,
            "image": "quay.io/mongodb/mongodb-agent:10.29.0.6830-1",
        },
        {
            "name": MONGOD_NAME,
            "image": "quay.io/mongodb/mongodb-enterprise-server:4.4.0-ubi8",
        },
        {
            "name": MONITORING_AGENT_NAME,
            "image": "quay.io/mongodb/mongodb-agent:10.29.0.6830-1",
        },
    ]

    if is_multi_cluster():
        enable_appdb_multi_cluster_deployment(resource)

    create_or_update(resource)
    return resource


@mark.e2e_om_appdb_configure_all_images
def test_appdb(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_om_appdb_configure_all_images
def test_om_get_started(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=400)
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=50)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_om_appdb_configure_all_images
def test_statefulset_spec_is_updated(ops_manager: MongoDBOpsManager):
    appdb_sts = ops_manager.read_appdb_statefulset()
    containers = appdb_sts.spec.template.spec.containers
    assert len(containers) == 3 if not is_multi_cluster() else 4

    agent_container = _get_container_by_name(AGENT_NAME, containers)

    assert agent_container is not None
    assert agent_container.image == "quay.io/mongodb/mongodb-agent:10.29.0.6830-1"

    mongod_container = _get_container_by_name(MONGOD_NAME, containers)

    assert mongod_container is not None
    assert mongod_container.image == "quay.io/mongodb/mongodb-enterprise-server:4.4.0-ubi8"

    monitoring_container = _get_container_by_name(MONITORING_AGENT_NAME, containers)

    assert monitoring_container is not None
    assert monitoring_container.image == "quay.io/mongodb/mongodb-agent:10.29.0.6830-1"


def _get_container_by_name(name: str, containers: List[V1Container]) -> Optional[V1Container]:
    return next(filter(lambda c: c.name == name, containers))
