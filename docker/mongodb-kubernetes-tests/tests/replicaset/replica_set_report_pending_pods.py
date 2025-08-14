from kubetester import delete_pod, delete_pvc
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = True
    return resource.create()


@mark.e2e_replica_set_report_pending_pods
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_replica_set_report_pending_pods
def test_cr_reports_pod_pending_status(replica_set: MongoDB, namespace: str):
    """delete the 0th pod and it's corresponding pvc to make sure the
    pod fails to enter ready state. Another way would be to delete the nodes in the cluster
    which makes the pod  to be unschedulable. The former process is just easier to reproduce."""
    delete_pod(namespace, "my-replica-set-0")
    delete_pvc(namespace, "data-my-replica-set-0")
    replica_set.assert_reaches_phase(Phase.Pending, timeout=600)
