import pytest
from kubetester.kubetester import skip_if_local
from kubetester.omtester import OMTester

from tests.opsmanager.om_base import OpsManagerBase

gen_key_resource_version = None
admin_key_resource_version = None


# Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
# creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
# updates in one test

# Current test should contain all kinds of upgrades to Ops Manager as a sequence of tests

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
        secret = self.corev1.read_namespaced_secret(self.om_cr.gen_key_secret(), self.namespace)
        data = secret.data
        assert "gen.key" in data
        # saving the resource version for later checks against updates
        gen_key_resource_version = secret.metadata.resource_version

    def test_admin_key_secret(self):
        global admin_key_resource_version
        secret = self.corev1.read_namespaced_secret(self.om_cr.api_key_secret(), self.namespace)
        data = secret.data
        assert "publicApiKey" in data
        assert "user" in data
        # saving the resource version for later checks against updates
        admin_key_resource_version = secret.metadata.resource_version

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
        gen_key_secret = self.corev1.read_namespaced_secret(self.om_cr.gen_key_secret(), self.namespace)
        api_key_secret = self.corev1.read_namespaced_secret(self.om_cr.api_key_secret(), self.namespace)

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
      The OM version is upgraded - this means the new image is deployed, the app db stays the same
    update:
      file: om_ops_manager_upgrade.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.2.0.56536.20190809T1110Z-1"}]'
      wait_until: om_in_running_state
      timeout: 500
    """

    def test_image_url(self):
        pod = self.corev1.read_namespaced_pod("om-upgrade-0", self.namespace)
        assert "4.2.0.56536.20190809T1110Z-1" in pod.spec.containers[0].image

    @skip_if_local
    def test_om(self):
        OMTester(self.om_context).assert_healthiness()

