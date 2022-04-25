import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ReplicaSetTester


@pytest.mark.e2e_replica_set_8_members
class TestReplicaSetEightMembers(KubernetesTester):
    """
    name: Big Replica set (8 members)
    description: |
        Tests that a replica set with > 7 members can be successfully created
    create:
      file: replica-set-8-members.yaml
      wait_until: in_running_state
      timeout: 360
    """

    def test_rs_ready(self):
        ReplicaSetTester("big-replica-set", 8).assert_connectivity()
