from os import environ

from time import sleep

import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from pytest import fixture

from kubetester import MongoDB
from kubetester.kubetester import fixture as yaml_fixture, run_periodically
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager

gen_key_resource_version = None
admin_key_resource_version = None
EXPECTED_VERSION = "4.2.8"


# Current test should contain all kinds of upgrades to Ops Manager as a sequence of tests
# TODO add the check for real OM version after upgrade (using the data in HTTP headers from the API calls)


@fixture(scope="module")
def ops_manager(namespace) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_upgrade.yaml"), namespace=namespace
    )

    return resource.create()


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerCreation:
    """
      Creates an Ops Manager instance with AppDB of size 3.
    """

    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_gen_key_secret(self, ops_manager: MongoDBOpsManager):
        global gen_key_resource_version
        secret = ops_manager.read_gen_key_secret()
        data = secret.data
        assert "gen.key" in data
        # saving the resource version for later checks against updates
        gen_key_resource_version = secret.metadata.resource_version

    def test_admin_key_secret(self, ops_manager: MongoDBOpsManager):
        global admin_key_resource_version
        secret = ops_manager.read_api_key_secret()
        data = secret.data
        assert "publicApiKey" in data
        assert "user" in data
        # saving the resource version for later checks against updates
        admin_key_resource_version = secret.metadata.resource_version

    def test_backup_not_enabled(self, ops_manager: MongoDBOpsManager):
        """ Backup is deliberately disabled so no statefulset should be created"""
        with pytest.raises(client.rest.ApiException):
            ops_manager.read_backup_statefulset()

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager):
        """Checks that the OM is responsive and test service is available (enabled by 'mms.testUtil.enabled')."""
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()

        om_tester.assert_test_service()
        try:
            om_tester.assert_support_page_enabled()
            pytest.xfail("mms.helpAndSupportPage.enabled is expected to be false")
        except AssertionError:
            pass


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerWithMongoDB:
    @staticmethod
    @fixture(scope="class")
    def mdb(ops_manager: MongoDBOpsManager):
        return (
            MongoDB.from_yaml(
                yaml_fixture("replica-set-for-om.yaml"),
                namespace=ops_manager.namespace,
                name="my-replica-set",
            )
            .configure(ops_manager, "development")
            .create()
        )

    def test_can_use_om(self, mdb):
        mdb.assert_reaches_phase(Phase.Running, timeout=350)
        mdb.assert_connectivity()

    def test_om_can_change_mongodb_version(self, mdb):
        mdb["spec"]["version"] = "4.2.1"

        mdb.update()
        mdb.assert_abandons_phase(Phase.Running)
        mdb.assert_reaches_phase(Phase.Running)
        mdb.assert_connectivity()
        mdb.tester().assert_version("4.2.1")


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
        ops_manager.om_status().assert_abandons_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=500)

    def test_keys_not_modified(self, ops_manager: MongoDBOpsManager):
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


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerVersionUpgrade:
    """
      The OM version is upgraded - this means the new image is deployed for both OM and appdb.
      >> Dev note: Please change the value of the new version to the latest one as soon as the new OM
       is released and its version is added to release.json
    """

    def test_upgrade_om_version(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["version"] = EXPECTED_VERSION
        if "CUSTOM_OM_VERSION" in environ:
            ops_manager["spec"]["version"] = environ.get("CUSTOM_OM_VERSION")
        ops_manager.update()
        ops_manager.om_status().assert_abandons_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_image_url(self, ops_manager: MongoDBOpsManager):
        pods = ops_manager.read_om_pods()
        assert len(pods) == 1
        assert ops_manager.get_version() in pods[0].spec.containers[0].image

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_om_tester().assert_healthiness()
        # TODO ideally we need to check the OM version as well but currently public API calls don't return the version
        # properly: "X-MongoDB-Service-Version: gitHash=ca9b4ac974b67f3f4c26563f94832037b0555829; versionString=current"


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
        sleep(5)

    def test_api_key_removed(self, ops_manager: MongoDBOpsManager):
        with pytest.raises(ApiException):
            ops_manager.read_api_key_secret()

    def test_gen_key_not_removed(self, ops_manager: MongoDBOpsManager):
        """ The gen key must not be removed - this is for situations when the appdb is persistent -
        so PVs may survive removal"""
        gen_key_secret = ops_manager.read_gen_key_secret()
        assert gen_key_secret.metadata.resource_version == gen_key_resource_version

    def test_om_sts_removed(self, ops_manager: MongoDBOpsManager):
        with pytest.raises(ApiException):
            ops_manager.read_statefulset()

    def test_om_appdb_removed(self, ops_manager: MongoDBOpsManager):
        with pytest.raises(ApiException):
            ops_manager.read_appdb_statefulset()
