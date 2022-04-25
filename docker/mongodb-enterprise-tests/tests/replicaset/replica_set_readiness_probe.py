import time

import pytest
from kubetester.kubetester import KubernetesTester, get_pods, skip_if_local
from kubetester.mongotester import ReplicaSetTester


@pytest.mark.e2e_replica_set_readiness_probe
class TestReplicaSetNoAgentDeadlock(KubernetesTester):
    """
    name: ReplicaSet recovers when all pods are removed
    description: |
      Creates a 2-members replica set and then removes the pods. The pods are started sequentially (pod-0 waits for
      pod-1 to get ready) but the AA in pod-1 needs pod-0 to be running to initialize replica set. The readiness probe
      must be clever enough to mark the pod "ready" if the agents is waiting for the other pods.
    create:
      file: replica-set-double.yaml
      wait_until: in_running_state
      timeout: 120
    """

    @skip_if_local()
    def test_db_connectable(self):
        ReplicaSetTester("my-replica-set-double", 2).assert_connectivity()

    def test_remove_pods_and_wait_for_recovery(self):
        pods = get_pods("my-replica-set-double-{}", 2)
        for podname in pods:
            self.corev1.delete_namespaced_pod(podname, self.namespace)

            print("\nRemoved pod {}".format(podname))

        # sleeping for 5 seconds to let the pods be removed
        time.sleep(5)

        # waiting until the pods recover and init the replica set again
        KubernetesTester.wait_until(TestReplicaSetNoAgentDeadlock.pods_are_ready, 120)

    @skip_if_local()
    def test_db_connectable_after_recovery(self):
        ReplicaSetTester("my-replica-set-double", 2).assert_connectivity()

    @staticmethod
    def pods_are_ready():
        sts = KubernetesTester.clients("appsv1").read_namespaced_stateful_set(
            "my-replica-set-double", KubernetesTester.get_namespace()
        )

        return sts.status.ready_replicas == 2
