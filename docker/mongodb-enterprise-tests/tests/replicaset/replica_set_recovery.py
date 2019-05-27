import pytest

from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_replica_set_recovery
class TestReplicaSetBadStateCreation(KubernetesTester):
    '''
    name: Replica Set Bad State Creation
    tags: replica-set, creation
    description: |
      Creates a Replica set with a bad configuration (wrong mongodb version) and ensures it enters a failed state
    create:
      file: replica-set-invalid.yaml
      wait_until: in_error_state
      timeout: 180
    '''

    def test_in_error_state(self):
        mrs = KubernetesTester.get_resource()
        assert mrs['status']['phase'] == 'Failed'
        assert mrs['status']['message'] in ('Failed to create/update replica set in Ops Manager: Status: 400 (Bad Request), Detail: Something went wrong validating your Automation Config. Sorry!',
                                            'Failed to create/update replica set in Ops Manager: Status: 500 (Internal Server Error), ErrorCode: UNEXPECTED_ERROR, Detail: Unexpected error.')


@pytest.mark.e2e_replica_set_recovery
class TestReplicaSetRecoversFromBadState(KubernetesTester):
    '''
    name: Replica Set Bad State Recovery
    tags: replica-set, creation
    description: |
      Updates spec of replica set in a bad state and ensures it is updated to the running state correctly
    update:
      file: replica-set-invalid.yaml
      patch: '[{"op":"replace","path":"/spec/version","value":"4.0.0"}]'
      wait_until: in_running_state
      timeout: 120
    '''

    def test_in_running_state(self):
        mrs = KubernetesTester.get_resource()
        status = mrs['status']
        assert status['phase'] == "Running"
        assert status['version'] == '4.0.0'
        assert "message" not in status
