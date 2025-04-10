import time

import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import get_pods, skip_if_local
from kubetester.mongodb import MongoDB, Phase

RESOURCE_NAME = "my-replica-set-double"


@pytest.fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-double.yaml"), RESOURCE_NAME, namespace)
    resource.set_architecture_annotation()
    return resource.update()


@pytest.fixture(scope="class")
def config_version():
    class ConfigVersion:
        def __init__(self):
            self.version = 0

    return ConfigVersion()


@pytest.mark.e2e_replica_set_readiness_probe
class TestReplicaSetNoAgentDeadlock(KubernetesTester):
    """
    name: ReplicaSet recovers when all pods are removed
    description: |
      Creates a 2-members replica set and then removes the pods. The pods are started sequentially (pod-0 waits for
      pod-1 to get ready) but the AA in pod-1 needs pod-0 to be running to initialize replica set. The readiness probe
      must be clever enough to mark the pod "ready" if the agents is waiting for the other pods.
    """

    def test_mdb_created(self, replica_set: MongoDB, config_version):
        replica_set.assert_reaches_phase(Phase.Running)
        config_version.version = self.get_automation_config()["version"]

    @skip_if_local()
    def test_db_connectable(self, replica_set: MongoDB):
        replica_set.assert_connectivity()

    def test_remove_pods_and_wait_for_recovery(self, config_version):
        pods = get_pods(RESOURCE_NAME + "-{}", 2)
        for podname in pods:
            self.corev1.delete_namespaced_pod(podname, self.namespace)

            print("\nRemoved pod {}".format(podname))

        # sleeping for 5 seconds to let the pods be removed
        time.sleep(5)

        # waiting until the pods recover and init the replica set again
        KubernetesTester.wait_until(TestReplicaSetNoAgentDeadlock.pods_are_ready, 120)
        assert self.get_automation_config()["version"] == config_version.version

    @skip_if_local()
    def test_db_connectable_after_recovery(self, replica_set: MongoDB):
        replica_set.assert_connectivity()

    def test_replica_set_recovered(self, replica_set: MongoDB, config_version):
        replica_set.assert_reaches_phase(Phase.Running)
        assert self.get_automation_config()["version"] == config_version.version

    @staticmethod
    def pods_are_ready():
        sts = KubernetesTester.clients("appsv1").read_namespaced_stateful_set(
            "my-replica-set-double", KubernetesTester.get_namespace()
        )

        return sts.status.ready_replicas == 2
