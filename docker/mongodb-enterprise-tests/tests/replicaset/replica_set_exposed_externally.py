import pytest

from pytest import fixture
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-externally-exposed.yaml"),
        "my-replica-set-externally-exposed",
        namespace,
    )
    resource.set_version(custom_mdb_version)
    yield resource.create()


@pytest.mark.e2e_replica_set_exposed_externally
def test_replica_set_created(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)


def test_nodeport_service_exists(namespace: str):
    service = client.CoreV1Api().read_namespaced_service(
        "my-replica-set-externally-exposed-svc-external", namespace
    )
    assert service.spec.type == "NodePort"
    assert service.spec.ports[0].port == 27017
    assert service.spec.ports[0].node_port


@pytest.mark.e2e_replica_set_exposed_externally
def test_nodeport_service_node_port_stays_the_same(
    namespace: str, replica_set: MongoDB
):
    service = client.CoreV1Api().read_namespaced_service(
        "my-replica-set-externally-exposed-svc-external", namespace
    )
    node_port = service.spec.ports[0].node_port

    replica_set.load()
    replica_set["spec"]["members"] = 2
    replica_set.update()

    replica_set.assert_abandons_phase(Phase.Running, timeout=60)
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)

    service = client.CoreV1Api().read_namespaced_service(
        "my-replica-set-externally-exposed-svc-external", namespace
    )
    assert service.spec.type == "NodePort"
    assert service.spec.ports[0].node_port == node_port


@pytest.mark.e2e_replica_set_exposed_externally
def test_service_gets_deleted(replica_set: MongoDB, namespace: str):

    replica_set.load()
    last_transition = replica_set.get_status_last_transition_time()
    replica_set["spec"]["exposedExternally"] = False
    replica_set.update()

    replica_set.assert_state_transition_happens(last_transition)
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)
    with pytest.raises(client.rest.ApiException):
        client.CoreV1Api().read_namespaced_service(
            "my-replica-set-externally-exposed-svc-external", namespace
        )
