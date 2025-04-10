import pytest
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import StandaloneTester


@pytest.fixture(scope="module")
def standalone(namespace: str, custom_mdb_prev_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("standalone-downgrade.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_prev_version)
    return resource.update()


@pytest.mark.e2e_standalone_upgrade_downgrade
class TestStandaloneUpgradeDowngradeCreate(KubernetesTester):
    """
    name: Standalone upgrade downgrade (create)
    description: |
      Creates a standalone, then upgrades it with compatibility version set and then downgrades back
    """

    def test_create_standalone(self, standalone: MongoDB):
        standalone.assert_reaches_phase(Phase.Running)

    @skip_if_local
    def test_db_connectable(self, custom_mdb_prev_version: str):
        mongod_tester = StandaloneTester("my-standalone-downgrade")
        mongod_tester.assert_version(custom_mdb_prev_version)

    def test_noop(self):
        assert True


@pytest.mark.e2e_standalone_upgrade_downgrade
class TestStandaloneUpgradeDowngradeUpdate(KubernetesTester):
    """
    name: Standalone upgrade downgrade (update)
    description: |
      Updates a Standalone to bigger version, leaving feature compatibility version as it was
    """

    def test_upgrade_standalone(self, standalone: MongoDB, custom_mdb_prev_version: str, custom_mdb_version: str):
        fcv = fcv_from_version(custom_mdb_prev_version)

        standalone.load()
        standalone.set_version(custom_mdb_version)
        standalone["spec"]["featureCompatibilityVersion"] = fcv
        standalone.update()

        standalone.assert_reaches_phase(Phase.Running)

    @skip_if_local
    def test_db_connectable(self, custom_mdb_version):
        mongod_tester = StandaloneTester("my-standalone-downgrade")
        mongod_tester.assert_version(custom_mdb_version)


@pytest.mark.e2e_standalone_upgrade_downgrade
class TestStandaloneUpgradeDowngradeRevert(KubernetesTester):
    """
    name: Standalone upgrade downgrade (downgrade)
    description: |
      Updates a Standalone to the same version it was created initially
    """

    def test_downgrade_standalone(self, standalone: MongoDB, custom_mdb_prev_version: str):
        standalone.load()
        standalone.set_version(custom_mdb_prev_version)
        standalone.update()

        standalone.assert_reaches_phase(Phase.Running)

    @skip_if_local
    def test_db_connectable(self, custom_mdb_prev_version):
        mongod_tester = StandaloneTester("my-standalone-downgrade")
        mongod_tester.assert_version(custom_mdb_prev_version)

    def test_noop(self):
        assert True
