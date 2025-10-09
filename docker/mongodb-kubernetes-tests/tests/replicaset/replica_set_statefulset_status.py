from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
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
        name=replica_set.name,
        msg_regexp="Not all the Pods are ready \(wanted: 2.*\)",
        idx=0,
    )
    replica_set.assert_reaches_phase(Phase.Pending, timeout=120)
    assert replica_set.get_status_message() == "StatefulSet not ready"


@mark.e2e_replica_set_statefulset_status
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)
    assert replica_set.get_status_resources_not_ready() is None
    assert replica_set.get_status_message() is None
