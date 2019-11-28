import pytest

from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_standalone_type_change_recovery
class TestStandaloneTypeChangeRecovery(KubernetesTester):
    """
    name: Standalone Type Change Recovery
    tags: standalone, creation
    description: |
      Creates a standalone, applies a valid ReplicaSet configuration attempting to change type,
      changes back and is in a successful state.
    create:
      file: standalone.yaml
      wait_until: in_running_state
    """

    def test_created_ok(self):
        assert True


@pytest.mark.e2e_standalone_type_change_recovery
class TestChangingStandaloneToReplicaSetFails(KubernetesTester):
    """
    name: Standalone Type Change Recovery
    tags: standalone, creation
    description: |
      Tries to change standalone to valid ReplicaSet configuration, this should fail.
    update:
      file: standalone.yaml
      patch: '[{"op":"add","path":"/spec/members","value":1}, {"op":"replace","path":"/spec/type","value":"ReplicaSet"}]'
      wait_until: in_error_state
    """

    def test_type_change_invalid(self):
        res = KubernetesTester.get_resource()
        assert (
            res["status"]["message"]
            == "Changing type is not currently supported, please change the resource back to a Standalone"
        )
        assert res["status"]["type"] == "Standalone"  # we haven't changed type


@pytest.mark.e2e_standalone_type_change_recovery
class TestChangingBackToStandaloneWorks(KubernetesTester):
    """
    name: Standalone Type Change Recovery
    tags: standalone, creation
    description: |
      Changes back to a standalone
    update:
      file: standalone.yaml
      patch: '[{"op":"replace","path":"/spec/type","value":"Standalone"}]'
      wait_until: in_running_state
    """

    def test_change_back_successful(self):
        res = KubernetesTester.get_resource()
        assert res["status"]["type"] == "Standalone"
