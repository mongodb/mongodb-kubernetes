import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import MongoDBOpsManager, MongoDB
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import Phase
from kubetester.omtester import OMTester
from pytest import fixture
from tests.opsmanager.om_base import OpsManagerBase

gen_key_resource_version = None
admin_key_resource_version = None


# Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
# creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
# updates in one test

# Current test should contain all kinds of upgrades to Ops Manager as a sequence of tests

# TODO add the check for real OM version after upgrade (using the data in HTTP headers from the API calls)


@fixture(scope="module")
def ops_manager(namespace) -> MongoDBOpsManager:
    # TODO: this is used only for loading the Ops Manager, the creation of OM is still done the old way
    return MongoDBOpsManager("om-upgrade", namespace).load()


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerCreation(OpsManagerBase):
    """
    name: Ops Manager successful creation
    description: |
      Creates an Ops Manager instance with AppDB of size 3.
    create:
      file: om_ops_manager_upgrade.yaml
      wait_until: om_in_running_state
      timeout: 900
    """

    def test_gen_key_secret(self):
        global gen_key_resource_version
        secret = self.corev1.read_namespaced_secret(
            self.om_cr.gen_key_secret(), self.namespace
        )
        data = secret.data
        assert "gen.key" in data
        # saving the resource version for later checks against updates
        gen_key_resource_version = secret.metadata.resource_version

    def test_admin_key_secret(self):
        global admin_key_resource_version
        secret = self.corev1.read_namespaced_secret(
            self.om_cr.api_key_secret(), self.namespace
        )
        data = secret.data
        assert "publicApiKey" in data
        assert "user" in data
        # saving the resource version for later checks against updates
        admin_key_resource_version = secret.metadata.resource_version

    def test_backup_not_enabled(self):
        """ Backup is deliberately disabled so no statefulset should be created"""
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set_status(
                self.om_cr.backup_sts_name(), self.namespace
            )

    @skip_if_local
    def test_om(self):
        """Checks that the OM is responsive and test service is available (enabled by 'mms.testUtil.enabled')."""
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()

        om_tester.assert_test_service()
        try:
            om_tester.assert_support_page_enabled()
            pytest.xfail("mms.helpAndSupportPage.enabled is expected to be false")
        except AssertionError:
            pass


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerWithMongoDB(OpsManagerBase):
    @staticmethod
    @fixture(scope="class")
    def mdb(ops_manager, namespace):
        return (
            MongoDB.from_yaml(
                yaml_fixture("replica-set-for-om.yaml"),
                namespace=namespace,
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
        mdb._tester().assert_version("4.2.1")


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerConfigurationChange(OpsManagerBase):
    """
    name: Ops Manager configuration changes
    description: |
      The OM configuration changes: one property is removed, another is added.
      Note, that this is quite artificial change to make it testable, these properties affect the behavior of different
      endpoints in Ops Manager, so we can then check if the changes were propagated to OM
    update:
      file: om_ops_manager_upgrade.yaml
      patch: '[{"op":"replace","path":"/spec/configuration/mms.testUtil.enabled", "value": null }, {"op":"add","path":"/spec/configuration/mms.helpAndSupportPage.enabled","value": "true"}]'
      wait_until: om_in_running_state
      timeout: 500
    """

    def test_keys_not_modified(self):
        """Making sure that the new reconciliation hasn't tried to generate new gen and api keys """
        gen_key_secret = self.corev1.read_namespaced_secret(
            self.om_cr.gen_key_secret(), self.namespace
        )
        api_key_secret = self.corev1.read_namespaced_secret(
            self.om_cr.api_key_secret(), self.namespace
        )

        assert gen_key_secret.metadata.resource_version == gen_key_resource_version
        assert api_key_secret.metadata.resource_version == admin_key_resource_version

    @skip_if_local
    def test_om(self):
        """Checks that the OM is responsive and test service is not available"""
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()
        om_tester.assert_support_page_enabled()
        try:
            om_tester.assert_test_service()
            pytest.xfail("mms.testUtil.enabled is expected to be false")
        except AssertionError:
            pass


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerVersionUpgrade(OpsManagerBase):
    """
    name: Ops Manager image upgrade
    description: |
      The OM version is upgraded - this means the new image is deployed for both OM and appdb.
      >> Dev note: Please change the value of the new version to the latest one as soon as the new OM
       is released and its version is added to release.json
    update:
      file: om_ops_manager_upgrade.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.2.7"}]'
      wait_until: om_in_running_state
      timeout: 1200
    """

    def test_image_url(self):
        pod = self.corev1.read_namespaced_pod("om-upgrade-0", self.namespace)
        assert "4.2.7" in pod.spec.containers[0].image

    @skip_if_local
    def test_om(self):
        OMTester(self.om_context).assert_healthiness()
        # TODO ideally we need to check the OM version as well but currently public API calls don't return the version
        # properly: "X-MongoDB-Service-Version: gitHash=ca9b4ac974b67f3f4c26563f94832037b0555829; versionString=current"


@pytest.mark.e2e_om_ops_manager_upgrade
class TestOpsManagerRemoved(OpsManagerBase):
    """
    name: Ops Manager removal
    description: |
      Deletes an Ops Manager Custom resource and verifies that some of the dependant objects are removed
    delete:
      file: om_ops_manager_upgrade.yaml
      wait_until: om_is_deleted
      timeout: 20
    """

    def test_api_key_removed(self):
        with pytest.raises(ApiException):
            self.corev1.read_namespaced_secret(
                self.om_cr.api_key_secret(), self.namespace
            )

    def test_gen_key_not_removed(self):
        """ The gen key must not be removed - this is for situations when the appdb is persistent -
        so PVs may survive removal"""
        self.corev1.read_namespaced_secret(self.om_cr.gen_key_secret(), self.namespace)

    def test_om_sts_removed(self):
        with pytest.raises(ApiException):
            self.appsv1.read_namespaced_stateful_set(self.om_cr.name(), self.namespace)

    def test_om_appdb_removed(self):
        with pytest.raises(ApiException):
            self.appsv1.read_namespaced_stateful_set(
                self.om_cr.app_db_name(), self.namespace
            )
