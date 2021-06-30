from time import sleep
from typing import Optional

import pytest
import semver
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import MongoDB
from kubetester.kubetester import fixture as yaml_fixture, run_periodically
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.opsmanager.om_appdb_scram import OM_USER_NAME

OM_CURRENT_VERSION = "4.2.13"
MDB_CURRENT_VERSION = "4.2.1-ent"

# Current test focuses on Ops Manager upgrade which involves upgrade for both OpsManager and AppDB.
# MongoDBs are also upgraded. In case of minor OM version upgrade (4.2 -> 4.4) agents are expected to be upgraded
# for the existing MongoDBs.


@fixture(scope="module")
def ops_manager(namespace) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_upgrade.yaml"), namespace=namespace
    )
    resource.set_version(OM_CURRENT_VERSION)
    resource.set_appdb_version(MDB_CURRENT_VERSION)

    return resource.create()


@fixture(scope="module")
def mdb(ops_manager: MongoDBOpsManager) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name="my-replica-set",
    )
    resource["spec"]["version"] = MDB_CURRENT_VERSION
    resource.configure(ops_manager, "development")
    return resource.create()

@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerCreation:
    """
    Creates an Ops Manager instance with AppDB of size 3.
    """

    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        # Monitoring
        ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=50)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)

    def test_gen_key_secret(self, ops_manager: MongoDBOpsManager):
        secret = ops_manager.read_gen_key_secret()
        data = secret.data
        assert "gen.key" in data

    def test_admin_key_secret(self, ops_manager: MongoDBOpsManager):
        secret = ops_manager.read_api_key_secret()
        data = secret.data
        assert "publicApiKey" in data
        assert "user" in data

    def test_backup_not_enabled(self, ops_manager: MongoDBOpsManager):
        """ Backup is deliberately disabled so no statefulset should be created"""
        with pytest.raises(client.rest.ApiException):
            ops_manager.read_backup_statefulset()

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager):
        """Checks that the OM is responsive and test service is available (enabled by 'mms.testUtil.enabled')."""
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_version(OM_CURRENT_VERSION)

        om_tester.assert_test_service()
        try:
            om_tester.assert_support_page_enabled()
            pytest.xfail("mms.helpAndSupportPage.enabled is expected to be false")
        except AssertionError:
            pass

    def test_appdb_scram_sha(self, ops_manager: MongoDBOpsManager):
        """ Checks that 4.2 OM has both SCRAM-SHA-1 and SCRAM-SHA-256 enabled """
        auto_generated_password = ops_manager.read_appdb_generated_password()
        automation_config_tester = ops_manager.get_automation_config_tester()
        automation_config_tester.assert_authentication_mechanism_enabled(
            "MONGODB-CR", False
        )
        automation_config_tester.assert_authentication_mechanism_enabled(
            "SCRAM-SHA-256", False
        )
        ops_manager.get_appdb_tester().assert_scram_sha_authentication(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-1"
        )
        ops_manager.get_appdb_tester().assert_scram_sha_authentication(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-256"
        )

    def test_generations(self, ops_manager: MongoDBOpsManager):
        assert ops_manager.appdb_status().get_observed_generation() == 1
        assert ops_manager.om_status().get_observed_generation() == 1
        assert ops_manager.backup_status().get_observed_generation() == 1


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerWithMongoDB:
    def test_mongodb_create(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=350)
        mdb.assert_connectivity()
        mdb.tester().assert_version(MDB_CURRENT_VERSION)

    def test_mongodb_upgrade(self, mdb: MongoDB):
        """Scales up the mongodb. Note, that we are not upgrading the Mongodb version at this stage as it can be
        the major update (e.g. 4.2 -> 4.4) and this requires OM upgrade as well - this happens later."""
        mdb["spec"]["members"] = 4

        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)
        mdb.assert_connectivity()
        assert mdb.get_status_members() == 4


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerConfigurationChange:
    """
    The OM configuration changes: one property is removed, another is added.
    Note, that this is quite artificial change to make it testable, these properties affect the behavior of different
    endpoints in Ops Manager, so we can then check if the changes were propagated to OM
    """

    def test_scale_app_db_up(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["configuration"]["mms.testUtil.enabled"] = ""
        ops_manager["spec"]["configuration"]["mms.helpAndSupportPage.enabled"] = "true"
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=500)

    def test_keys_not_modified(
        self,
        ops_manager: MongoDBOpsManager,
        gen_key_resource_version: str,
        admin_key_resource_version: str,
    ):
        """Making sure that the new reconciliation hasn't tried to generate new gen and api keys """
        gen_key_secret = ops_manager.read_gen_key_secret()
        api_key_secret = ops_manager.read_api_key_secret()

        assert gen_key_secret.metadata.resource_version == gen_key_resource_version
        assert api_key_secret.metadata.resource_version == admin_key_resource_version

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager):
        """Checks that the OM is responsive and test service is not available"""
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_support_page_enabled()
        try:
            om_tester.assert_test_service()
            pytest.xfail("mms.testUtil.enabled is expected to be false")
        except AssertionError:
            pass

    def test_generations(self, ops_manager: MongoDBOpsManager):
        assert ops_manager.appdb_status().get_observed_generation() == 2
        assert ops_manager.om_status().get_observed_generation() == 2
        assert ops_manager.backup_status().get_observed_generation() == 2


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerVersionUpgrade:
    """
    The OM version is upgraded - this means the new image is deployed for both OM and appdb.
    """

    agent_version = None

    def test_agent_version(self, mdb: MongoDB):
        TestOpsManagerVersionUpgrade.agent_version = (
            mdb.get_automation_config_tester().get_agent_version()
        )

    def test_upgrade_om_version(
        self,
        ops_manager: MongoDBOpsManager,
        custom_version: Optional[str],
        custom_appdb_version: str,
    ):
        ops_manager.load()
        ops_manager.set_version(custom_version)
        ops_manager.set_appdb_version(custom_appdb_version)

        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_image_url(self, ops_manager: MongoDBOpsManager):
        pods = ops_manager.read_om_pods()
        assert len(pods) == 1
        assert ops_manager.get_version() in pods[0].spec.containers[0].image

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_version(ops_manager.get_version())

    def test_appdb(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        mdb_tester = ops_manager.get_appdb_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version(custom_appdb_version)

    def test_appdb_scram_sha(self, ops_manager: MongoDBOpsManager):
        """ Right after OM was upgraded from 4.2 the AppDB still uses both SCRAM methods"""
        auto_generated_password = ops_manager.read_appdb_generated_password()
        automation_config_tester = ops_manager.get_automation_config_tester()
        automation_config_tester.assert_authentication_mechanism_enabled(
            "MONGODB-CR", False
        )
        automation_config_tester.assert_authentication_mechanism_enabled(
            "SCRAM-SHA-256", False
        )
        ops_manager.get_appdb_tester().assert_scram_sha_authentication(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-1"
        )
        ops_manager.get_appdb_tester().assert_scram_sha_authentication(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-256"
        )


@pytest.mark.e2e_om_ops_manager_upgrade
class TestMongoDbsVersionUpgrade:
    def test_mongodb_upgrade(self, mdb: MongoDB, custom_mdb_version: str):
        """Ensures that the existing MongoDB works fine with the new Ops Manager (scales up one member)
        Some details:
        - in case of patch upgrade of OM the existing agent is guaranteed to work with the new OM - we don't require
        the upgrade of all the agents
        - in case of major/minor OM upgrade the agents MUST be upgraded before reconciling - so that's why the agents upgrade
        is enforced before MongoDB reconciliation (the OM reconciliation happened above will drop the 'agents.nextScheduledTime'
        counter)
        """

        # TODO Remove this when making this a 4.4 to 5.0 upgrade test
        # It is needed only for OM 4.2 to avoid sending both a net.tls.mode
        # and a major/minor mdb upgrade
        # See https://github.com/10gen/ops-manager-kubernetes/pull/1623 for context
        mdb.reload()
        mdb["spec"]["logLevel"] = "DEBUG"

        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)
        mdb.reload()
        mdb["spec"]["version"] = custom_mdb_version

        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)
        mdb.assert_connectivity()
        mdb.tester().assert_version(custom_mdb_version)

    def test_agents_upgraded(self, mdb: MongoDB, ops_manager: MongoDBOpsManager):
        """The agents were requested to get upgraded immediately after Ops Manager upgrade.
        Note, that this happens only for OM major/minor upgrade, so we need to check only this case
        TODO CLOUDP-64622: we need to check the periodic agents upgrade as well - this can be done through Operator custom configuration"""
        prev_version = semver.VersionInfo.parse(OM_CURRENT_VERSION)
        new_version = semver.VersionInfo.parse(ops_manager.get_version())
        if (
            prev_version.major != new_version.major
            or prev_version.minor != new_version.minor
        ):
            assert (
                TestOpsManagerVersionUpgrade.agent_version
                != mdb.get_automation_config_tester().get_agent_version()
            )


@pytest.mark.e2e_om_ops_manager_upgrade
class TestAppDBScramShaUpdated:
    def test_appdb_reconcile(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["logLevel"] = "DEBUG"
        ops_manager.update()

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)

    @pytest.mark.skip(
        reason="re-enable when only SCRAM-SHA-256 is supported for the AppDB"
    )
    def test_appdb_scram_sha_(self, ops_manager: MongoDBOpsManager):
        """ In case of upgrade OM 4.2 -> OM 4.4 the AppDB scram-sha method must be upgraded as well """
        automation_config_tester = ops_manager.get_automation_config_tester()
        automation_config_tester.assert_authentication_mechanism_enabled(
            "SCRAM-SHA-256", False
        )
        automation_config_tester.assert_authentication_mechanism_disabled(
            "MONGODB-CR", False
        )


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerRemoved:
    """
    Deletes an Ops Manager Custom resource and verifies that some of the dependant objects are removed
    """

    def test_opsmanager_deleted(self, ops_manager: MongoDBOpsManager):
        ops_manager.delete()

        def om_is_clean():
            try:
                ops_manager.load()
                return False
            except ApiException:
                return True

        run_periodically(om_is_clean, timeout=180)
        # Some strange race conditions/caching - the api key secret is still queryable right after OM removal
        # (in openshift mainly)
        sleep(20)

    def test_api_key_removed(self, ops_manager: MongoDBOpsManager):
        with pytest.raises(ApiException):
            ops_manager.read_api_key_secret()

    def test_gen_key_not_removed(
        self, ops_manager: MongoDBOpsManager, gen_key_resource_version: str
    ):
        """The gen key must not be removed - this is for situations when the appdb is persistent -
        so PVs may survive removal"""
        gen_key_secret = ops_manager.read_gen_key_secret()
        assert gen_key_secret.metadata.resource_version == gen_key_resource_version

    def test_om_sts_removed(self, ops_manager: MongoDBOpsManager):
        with pytest.raises(ApiException):
            ops_manager.read_statefulset()

    def test_om_appdb_removed(self, ops_manager: MongoDBOpsManager):
        with pytest.raises(ApiException):
            ops_manager.read_appdb_statefulset()
