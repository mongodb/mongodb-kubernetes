import random

from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDBOpsManager, Phase
from pytest import fixture, mark


@fixture(scope="module")
def opsmanager(namespace: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_scale.yaml"), namespace=namespace
    )

    resource["spec"]["replicas"] = 1
    resource["spec"]["backup"] = {"enabled": False}

    yield resource.create()


@mark.e2e_om_external_connectivity
def test_reaches_goal_state(opsmanager: MongoDBOpsManager):
    opsmanager.assert_reaches_phase(Phase.Running, timeout=600)

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
    opsmanager["spec"]["externalConnectivity"] = ext_connectivity
    opsmanager.update()

    opsmanager.assert_abandons_phase(Phase.Running)
    opsmanager.assert_reaches_phase(Phase.Running)

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
    opsmanager["spec"]["externalConnectivity"]["annotations"] = annotations
    opsmanager.update()

    opsmanager.assert_abandons_phase(Phase.Running)
    opsmanager.assert_reaches_phase(Phase.Running)

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

    opsmanager.assert_reaches(
        lambda x: service_is_changed_to_nodeport(opsmanager.services())
    )

    internal, external = opsmanager.services()
    assert internal.spec.type == "ClusterIP"
    assert external.spec.type == "NodePort"
    assert external.spec.ports[0].node_port == node_port

    opsmanager["spec"]["externalConnectivity"] = {
        "type": "LoadBalancer",
    }
    opsmanager.update()

    opsmanager.assert_reaches(
        lambda x: service_is_changed_to_loadbalancer(opsmanager.services())
    )

    _, external = opsmanager.services()
    assert external.spec.type == "LoadBalancer"
    assert external.spec.ports[0].node_port == node_port


def service_is_changed_to_nodeport(services):
    return services[1].spec.type == "NodePort"


def service_is_changed_to_loadbalancer(services):
    return services[1].spec.type == "LoadBalancer"
