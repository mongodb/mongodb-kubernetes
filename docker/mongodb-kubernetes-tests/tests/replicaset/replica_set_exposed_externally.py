import pytest
from kubernetes import client
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture
from tests.common.placeholders import placeholders


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-externally-exposed.yaml"),
        "my-replica-set-externally-exposed",
        namespace,
    )
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_exposed_externally
def test_replica_set_created(replica_set: MongoDB, custom_mdb_version: str):
    replica_set["spec"]["members"] = 2
    replica_set.set_version(custom_mdb_version)
    replica_set.update()

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
    service = client.CoreV1Api().read_namespaced_service("my-replica-set-externally-exposed-0-svc-external", namespace)
    node_port = service.spec.ports[0].node_port

    replica_set.load()
    replica_set["spec"]["members"] = 3
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=300)

    service = client.CoreV1Api().read_namespaced_service("my-replica-set-externally-exposed-0-svc-external", namespace)
    assert service.spec.type == "LoadBalancer"
    assert service.spec.ports[0].node_port == node_port


@pytest.mark.e2e_replica_set_exposed_externally
def test_placeholders_in_external_services(namespace: str, replica_set: MongoDB):
    external_access = replica_set["spec"].get("externalAccess", {})
    external_service = external_access.get("externalService", {})
    external_service["annotations"] = placeholders.get_annotations_with_placeholders_for_single_cluster()
    external_access["externalService"] = external_service
    replica_set["spec"]["externalAccess"] = external_access
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)

    name = replica_set["metadata"]["name"]
    for pod_idx in range(0, replica_set.get_members()):
        service = client.CoreV1Api().read_namespaced_service(f"{name}-{pod_idx}-svc-external", namespace)
        assert service.metadata.annotations == placeholders.get_expected_annotations_single_cluster(
            name, namespace, pod_idx
        )


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
            client.CoreV1Api().read_namespaced_service(f"my-replica-set-externally-exposed-{i}-svc-external", namespace)
