from typing import Optional
import random

from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark


@fixture(scope="module")
def opsmanager(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    yield resource.create()


@mark.e2e_om_external_connectivity
def test_reaches_goal_state(opsmanager: MongoDBOpsManager):
    opsmanager.om_status().assert_reaches_phase(Phase.Running, timeout=600)
    # some time for monitoring to be finished
    opsmanager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    opsmanager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)
    opsmanager.om_status().assert_reaches_phase(Phase.Running, timeout=50)

    internal, external = opsmanager.services()
    assert internal is not None
    assert external is None

    assert internal.spec.type == "ClusterIP"
    assert internal.spec.cluster_ip == "None"


@mark.e2e_om_external_connectivity
def test_set_external_connectivity(opsmanager: MongoDBOpsManager):
    # TODO: The loadBalancerIP being set to 1.2.3.4 will not allow for this
    # LoadBalancer to work in Kops.
    ext_connectivity = {
        "type": "LoadBalancer",
        "loadBalancerIP": "1.2.3.4",
        "externalTrafficPolicy": "Local",
        "annotations": {
            "first-annotation": "first-value",
            "second-annotation": "second-value",
        },
    }
    opsmanager.load()
    opsmanager["spec"]["externalConnectivity"] = ext_connectivity
    opsmanager.update()

    opsmanager.om_status().assert_reaches_phase(Phase.Running)

    internal, external = opsmanager.services()

    assert internal is not None
    assert internal.spec.type == "ClusterIP"
    assert internal.spec.cluster_ip == "None"

    assert external is not None
    assert external.spec.type == "LoadBalancer"
    assert external.spec.load_balancer_ip == "1.2.3.4"
    assert external.spec.external_traffic_policy == "Local"


@mark.e2e_om_external_connectivity
def test_add_annotations(opsmanager: MongoDBOpsManager):
    """Makes sure annotations are updated properly."""
    annotations = {"second-annotation": "edited-value", "added-annotation": "new-value"}
    opsmanager.load()
    opsmanager["spec"]["externalConnectivity"]["annotations"] = annotations
    opsmanager.update()

    opsmanager.om_status().assert_reaches_phase(Phase.Running)

    internal, external = opsmanager.services()

    ant = external.metadata.annotations
    assert len(ant) == 3
    assert "first-annotation" in ant
    assert "second-annotation" in ant
    assert "added-annotation" in ant

    assert ant["second-annotation"] == "edited-value"
    assert ant["added-annotation"] == "new-value"


@mark.e2e_om_external_connectivity
def test_service_set_node_port(opsmanager: MongoDBOpsManager):
    """Changes externalConnectivity to type NodePort."""
    node_port = random.randint(30000, 32700)
    opsmanager["spec"]["externalConnectivity"] = {
        "type": "NodePort",
        "port": node_port,
    }
    opsmanager.update()

    opsmanager.assert_reaches(lambda om: service_is_changed_to_nodeport(om))

    internal, external = opsmanager.services()
    assert internal.spec.type == "ClusterIP"
    assert external.spec.type == "NodePort"
    assert external.spec.ports[0].node_port == node_port
    assert external.spec.ports[0].port == node_port
    assert external.spec.ports[0].target_port == 8080

    opsmanager["spec"]["externalConnectivity"] = {
        "type": "LoadBalancer",
        "port": node_port,
    }
    opsmanager.update()

    opsmanager.assert_reaches(lambda om: service_is_changed_to_loadbalancer(om))

    _, external = opsmanager.services()
    assert external.spec.type == "LoadBalancer"
    assert external.spec.ports[0].node_port == node_port
    assert external.spec.ports[0].port == node_port
    assert external.spec.ports[0].target_port == 8080


def service_is_changed_to_nodeport(om: MongoDBOpsManager) -> bool:
    return om.services()[1].spec.type == "NodePort"


def service_is_changed_to_loadbalancer(om: MongoDBOpsManager) -> bool:
    return om.services()[1].spec.type == "LoadBalancer"
