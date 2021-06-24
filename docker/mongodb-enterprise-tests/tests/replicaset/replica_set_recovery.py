import pytest

from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_replica_set_recovery
class TestReplicaSetBadStateCreation(KubernetesTester):
    """
    name: Replica Set Bad State Creation
    tags: replica-set, creation
    description: |
      Creates a Replica set with a bad configuration (wrong mongodb version) and ensures it enters a failed state
    create:
      file: replica-set-invalid.yaml
      wait_until: in_error_state
      timeout: 180
    """

    def test_in_error_state(self):
        mrs = KubernetesTester.get_resource()
        assert mrs["status"]["phase"] == "Failed"

        # Messages about a wrong autmationConfig changed from OM40 to OM42
        # This is the message emitted by the Operator
        assert (
            "Failed to create/update (Ops Manager reconciliation phase)"
            in mrs["status"]["message"]
        )


@pytest.mark.e2e_replica_set_recovery
class TestReplicaSetRecoversFromBadState(KubernetesTester):
    """
    name: Replica Set Bad State Recovery
    tags: replica-set, creation
    description: |
      Updates spec of replica set in a bad state and ensures it is updated to the running state correctly
    update:
      file: replica-set-invalid.yaml
      patch: '[{"op":"replace","path":"/spec/version","value":"4.0.1"}]'
      wait_until: in_running_state
      timeout: 240
    """

    def test_in_running_state(self):
        mrs = KubernetesTester.get_resource()
        status = mrs["status"]
        assert status["version"] == "4.0.1"
        assert "message" not in status
