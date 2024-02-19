import pytest
from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_replica_set_recovery
class TestReplicaSetBadStateCreation(KubernetesTester):
    """
    name: Replica Set Bad State Creation
    tags: replica-set, creation
    description: |
      Creates a Replica set with a bad configuration (wrong credentials) and ensures it enters a failed state
    create:
      file: replica-set-invalid.yaml
      wait_until: in_error_state
      timeout: 180
    """

    def test_in_error_state(self):
        mrs = KubernetesTester.get_resource()
        assert mrs["status"]["phase"] == "Failed"


@pytest.mark.e2e_replica_set_recovery
class TestReplicaSetRecoversFromBadState(KubernetesTester):
    """
    name: Replica Set Bad State Recovery
    tags: replica-set, creation
    description: |
      Updates spec of replica set in a bad state and ensures it is updated to the running state correctly
    update:
      file: replica-set-invalid.yaml
      patch: '[{"op":"replace","path":"/spec/credentials","value":"my-credentials"}]'
      wait_until: in_running_state
      timeout: 400
    """

    def test_in_running_state(self):
        mrs = KubernetesTester.get_resource()
        assert mrs["status"]["phase"] == "Running"
