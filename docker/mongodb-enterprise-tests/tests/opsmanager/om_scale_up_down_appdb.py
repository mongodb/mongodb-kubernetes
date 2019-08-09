import pytest
from kubetester.kubetester import skip_if_local
from kubetester.omtester import OMTester

from tests.opsmanager.om_base import OpsManagerBase

gen_key_resource_version = None
admin_key_resource_version = None

# Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
# creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
# updates in one test

@pytest.mark.e2e_om_scale_up_down_appdb
class TestOpsManagerCreation(OpsManagerBase):
    """
    name: Ops Manager successful creation
    description: |
      Creates an Ops Manager instance with AppDB of size 3. Note, that the initial creation usually takes ~500 seconds
    create:
      file: om_scale_up_down_appdb.yaml
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

    def test_appdb(self):
        assert self.om_cr.get_appdb_status()['members'] == 3
        assert self.om_cr.get_appdb_status()['version'] == '4.0.7'
        statefulset = self.appsv1.read_namespaced_stateful_set_status(self.om_cr.app_db_name(), self.namespace)
        assert statefulset.status.ready_replicas == 3
        assert statefulset.status.current_replicas == 3

    @skip_if_local
    def test_om_connectivity(self):
        OMTester(self.om_context).assert_healthiness()
        # todo check the backing db group, automation config and data integrity


@pytest.mark.e2e_om_scale_up_down_appdb
class TestOpsManagerAppDbScaleUp(OpsManagerBase):
    """
    name: Ops Manager successful appdb scale up
    description: |
      Scales appdb up to 5 members
    update:
      file: om_scale_up_down_appdb.yaml
      patch: '[{"op":"replace","path":"/spec/applicationDatabase/members","value":5}]'
      wait_until: om_in_running_state
      timeout: 400
    """

    def test_keys_not_touched(self):
        """Making sure that the new reconciliation hasn't tried to generate new gen and api keys """
        gen_key_secret = self.corev1.read_namespaced_secret(self.om_cr.gen_key_secret(), self.namespace)
        api_key_secret = self.corev1.read_namespaced_secret(self.om_cr.api_key_secret(), self.namespace)

        assert gen_key_secret.metadata.resource_version == gen_key_resource_version
        assert api_key_secret.metadata.resource_version == admin_key_resource_version

    def test_appdb(self):
        assert self.om_cr.get_appdb_status()['members'] == 5
        assert self.om_cr.get_appdb_status()['version'] == '4.0.7'
        statefulset = self.appsv1.read_namespaced_stateful_set_status(self.om_cr.app_db_name(), self.namespace)
        assert statefulset.status.ready_replicas == 5
        assert statefulset.status.current_replicas == 5

    @skip_if_local
    def test_om_connectivity(self):
        OMTester(self.om_context).assert_healthiness()

@pytest.mark.e2e_om_scale_up_down_appdb
class TestOpsManagerAppDbScaleDown(OpsManagerBase):
    """
    name: Ops Manager successful appdb scale down
    description: |
      Scales appdb back down to 3 members
    update:
      file: om_scale_up_down_appdb.yaml
      patch: '[{"op":"replace","path":"/spec/applicationDatabase/members","value":3}]'
      wait_until: om_in_running_state
      timeout: 400
    """

    def test_appdb(self):
        assert self.om_cr.get_appdb_status()['members'] == 3
        statefulset = self.appsv1.read_namespaced_stateful_set_status(self.om_cr.app_db_name(), self.namespace)
        assert statefulset.status.ready_replicas == 3
        assert statefulset.status.current_replicas == 3

    @skip_if_local
    def test_om_connectivity(self):
        OMTester(self.om_context).assert_healthiness()
