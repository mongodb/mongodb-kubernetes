import pytest
from kubetester.kubetester import KubernetesTester

@pytest.mark.e2e_replica_set_8_members
class TestReplicaSetEightMembers(KubernetesTester):
    '''
    name: Big Replica set (8 members)
    description: |
        Tests that a replica set with > 7 members can be successfully created
    create:
      file: replica-set-8-members.yaml
      wait_until: in_running_state
      timeout: 240
    '''

    def test_rs_ready(self):
        primary_available, secondaries_available = self.check_replica_set_is_ready("big-replica-set", replicas_count=8)

        assert primary_available, "primary was not available"
        assert secondaries_available, "secondaries not available"
