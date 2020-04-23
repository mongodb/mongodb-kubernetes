from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-double.yaml"),
        namespace=namespace,
        name="replica-set-status",
    )
    return resource.create()


@mark.e2e_replica_set_statefulset_status
def test_replica_set_reaches_pending_phase(replica_set: MongoDB):
    replica_set.wait_for(
        lambda s: s.get_status_resources_not_ready() is not None,
        timeout=150,
        should_raise=True,
    )
    # the StatefulSet name is equal to replica set name
    replica_set.assert_status_resource_not_ready(
        replica_set.name, msg_regexp="Not all the Pods are ready \(total: 2.*\)",
    )
    assert replica_set.get_status_phase() == Phase.Pending
    assert replica_set.get_status_message() == "StatefulSet not ready"


@mark.e2e_replica_set_statefulset_status
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    # The 'status.resourcesNotReady' must get cleaned soon after the replica set StatefulSet is ready - then
    # the resource will stay in 'Reconciling' phase for some time waiting for the agents to reach goal state
    replica_set.wait_for(
        lambda s: s.get_status_resources_not_ready() is None,
        timeout=150,
        should_raise=True,
    )
    assert replica_set.get_status_phase() == Phase.Reconciling
    assert replica_set.get_status_message() is None

    replica_set.assert_reaches_phase(Phase.Running, timeout=100)
    assert replica_set.get_status_resources_not_ready() is None
