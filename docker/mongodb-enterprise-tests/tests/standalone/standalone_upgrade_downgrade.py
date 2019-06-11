import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import StandaloneTester


@pytest.mark.e2e_standalone_upgrade_downgrade
class TestStandaloneUpgradeDowngradeCreate(KubernetesTester):
    '''
    name: Standalone upgrade downgrade (create)
    description: |
      Creates a standalone, then upgrades it with compatibility version set and then downgrades back
    create:
      file: standalone-downgrade.yaml
      wait_until: in_running_state
      timeout: 100
    '''


    def test_db_connectable(self):
        mongod_tester = StandaloneTester("my-standalone-downgrade")
        mongod_tester.assert_version("3.6.12")

@pytest.mark.e2e_standalone_upgrade_downgrade
class TestStandaloneUpgradeDowngradeUpdate(KubernetesTester):
    '''
    name: Standalone upgrade downgrade (update)
    description: |
      Updates a Standalone to bigger version, leaving feature compatibility version as it was
    update:
      file: standalone-downgrade.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.0.3"}, {"op":"add","path":"/spec/featureCompatibilityVersion", "value": "3.6"}]'
      wait_until: in_running_state
      timeout: 100
    '''

    def test_db_connectable(self):
        mongod_tester = StandaloneTester("my-standalone-downgrade")
        mongod_tester.assert_version("4.0.3")

@pytest.mark.e2e_standalone_upgrade_downgrade
class TestStandaloneUpgradeDowngradeRevert(KubernetesTester):
    '''
    name: Standalone upgrade downgrade (downgrade)
    description: |
      Updates a Standalone to the same version it was created initially
    update:
      file: standalone-downgrade.yaml
      wait_until: in_running_state
      timeout: 100
    '''

    def test_db_connectable(self):
        mongod_tester = StandaloneTester("my-standalone-downgrade")
        mongod_tester.assert_version("3.6.12")
