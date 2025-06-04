import random
from typing import Optional

from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def opsmanager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )

    if try_load(resource):
        return resource

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    ## Force creating headless services for internal connectivity
    resource["spec"]["internalConnectivity"] = {
        "type": "ClusterIP",
        "ClusterIP": "None",
    }
    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@mark.e2e_om_external_connectivity
def test_reaches_goal_state(opsmanager: MongoDBOpsManager):
    opsmanager.om_status().assert_reaches_phase(Phase.Running, timeout=600)
    # some time for monitoring to be finished
    opsmanager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    opsmanager.om_status().assert_reaches_phase(Phase.Running, timeout=50)

    for (
        cluster_idx,
        cluster_spec_item,
    ) in opsmanager.get_om_indexed_cluster_spec_items():
        internal, external = opsmanager.services(cluster_spec_item["clusterName"])
        assert internal is not None
        assert external is None
        assert internal.spec.type == "ClusterIP"
        assert internal.spec.cluster_ip == "None"


@mark.e2e_om_external_connectivity
def test_set_external_connectivity_load_balancer_with_default_port(
    opsmanager: MongoDBOpsManager,
):
    ext_connectivity = {
        "type": "LoadBalancer",
        "loadBalancerIP": "172.18.255.211",
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

    for _, cluster_spec_item in opsmanager.get_om_indexed_cluster_spec_items():
        internal, external = opsmanager.services(cluster_spec_item["clusterName"])

        assert internal is not None
        assert internal.spec.type == "ClusterIP"
        assert internal.spec.cluster_ip == "None"

        assert external is not None
        assert external.spec.type == "LoadBalancer"
        assert len(external.spec.ports) == 1
        assert external.spec.ports[0].port == 8080  # if not specified it will be the default port
        assert external.spec.load_balancer_ip == "172.18.255.211"
        assert external.spec.external_traffic_policy == "Local"


@mark.e2e_om_external_connectivity
def test_set_external_connectivity(opsmanager: MongoDBOpsManager):
    ext_connectivity = {
        "type": "LoadBalancer",
        "loadBalancerIP": "172.18.255.211",
        "externalTrafficPolicy": "Local",
        "port": 443,
        "annotations": {
            "first-annotation": "first-value",
            "second-annotation": "second-value",
        },
    }
    opsmanager.load()
    opsmanager["spec"]["externalConnectivity"] = ext_connectivity
    opsmanager.update()

    opsmanager.om_status().assert_reaches_phase(Phase.Running)

    for _, cluster_spec_item in opsmanager.get_om_indexed_cluster_spec_items():
        internal, external = opsmanager.services(cluster_spec_item["clusterName"])

        assert internal is not None
        assert internal.spec.type == "ClusterIP"
        assert internal.spec.cluster_ip == "None"

        assert external is not None
        assert external.spec.type == "LoadBalancer"
        assert len(external.spec.ports) == 1
        assert external.spec.ports[0].port == 443
        assert external.spec.load_balancer_ip == "172.18.255.211"
        assert external.spec.external_traffic_policy == "Local"


@mark.e2e_om_external_connectivity
def test_add_annotations(opsmanager: MongoDBOpsManager):
    """Makes sure annotations are updated properly."""
    annotations = {"second-annotation": "edited-value", "added-annotation": "new-value"}
    opsmanager.load()
    opsmanager["spec"]["externalConnectivity"]["annotations"] = annotations
    opsmanager.update()

    opsmanager.om_status().assert_reaches_phase(Phase.Running)

    for _, cluster_spec_item in opsmanager.get_om_indexed_cluster_spec_items():
        internal, external = opsmanager.services(cluster_spec_item["clusterName"])

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

    for _, cluster_spec_item in opsmanager.get_om_indexed_cluster_spec_items():
        internal, external = opsmanager.services(cluster_spec_item["clusterName"])
        assert internal.spec.type == "ClusterIP"
        assert external.spec.type == "NodePort"
        assert external.spec.ports[0].node_port == node_port
        assert external.spec.ports[0].port == node_port
        assert external.spec.ports[0].target_port == 8080

    opsmanager.load()
    opsmanager["spec"]["externalConnectivity"] = {
        "type": "LoadBalancer",
        "port": 443,
    }
    opsmanager.update()

    opsmanager.assert_reaches(lambda om: service_is_changed_to_loadbalancer(om))

    for _, cluster_spec_item in opsmanager.get_om_indexed_cluster_spec_items():
        _, external = opsmanager.services(cluster_spec_item["clusterName"])
        assert external.spec.type == "LoadBalancer"
        assert external.spec.ports[0].port == 443
        assert external.spec.ports[0].target_port == 8080


def service_is_changed_to_nodeport(om: MongoDBOpsManager) -> bool:
    for _, cluster_spec_item in om.get_om_indexed_cluster_spec_items():
        svc = om.services(cluster_spec_item["clusterName"])[1]
        if svc.spec.type != "NodePort":
            return False

    return True


def service_is_changed_to_loadbalancer(om: MongoDBOpsManager) -> bool:
    for _, cluster_spec_item in om.get_om_indexed_cluster_spec_items():
        svc = om.services(cluster_spec_item["clusterName"])[1]
        if svc.spec.type != "LoadBalancer":
            return False

    return True
