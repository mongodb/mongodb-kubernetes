import pytest

from pytest import fixture
from kubernetes import client

from kubetester import create_or_update
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-externally-exposed.yaml"),
        "my-replica-set-externally-exposed",
        namespace,
    )
    resource["spec"]["members"] = 2
    resource.set_version(custom_mdb_version)
    create_or_update(resource)
    return resource


@pytest.mark.e2e_replica_set_exposed_externally
def test_replica_set_created(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)


def test_service_exists(namespace: str):
    for i in range(2):
        service = client.CoreV1Api().read_namespaced_service(
            f"my-replica-set-externally-exposed-{i}-svc-external", namespace
        )
        assert service.spec.type == "LoadBalancer"
        assert service.spec.ports[0].port == 27017


@pytest.mark.e2e_replica_set_exposed_externally
def test_service_node_port_stays_the_same(namespace: str, replica_set: MongoDB):
    service = client.CoreV1Api().read_namespaced_service(
        "my-replica-set-externally-exposed-0-svc-external", namespace
    )
    node_port = service.spec.ports[0].node_port

    replica_set.load()
    replica_set["spec"]["members"] = 3
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=300)

    service = client.CoreV1Api().read_namespaced_service(
        "my-replica-set-externally-exposed-0-svc-external", namespace
    )
    assert service.spec.type == "LoadBalancer"
    assert service.spec.ports[0].node_port == node_port


@pytest.mark.e2e_replica_set_exposed_externally
def test_service_gets_deleted(replica_set: MongoDB, namespace: str):
    replica_set.load()
    last_transition = replica_set.get_status_last_transition_time()
    replica_set["spec"]["externalAccess"] = None
    replica_set.update()

    replica_set.assert_state_transition_happens(last_transition)
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)
    for i in range(replica_set["spec"]["members"]):
        with pytest.raises(client.rest.ApiException):
            client.CoreV1Api().read_namespaced_service(
                f"my-replica-set-externally-exposed-{i}-svc-external", namespace
            )
