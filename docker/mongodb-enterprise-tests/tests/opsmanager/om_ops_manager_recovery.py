import threading
import time

import pytest
import yaml
from kubernetes.client.rest import ApiException
from kubetester.kubetester import fixture, skip_if_local
from kubetester.omcr import OpsManagerCR
from kubetester.omtester import OMTester
from tests.opsmanager.om_base import OpsManagerBase


@pytest.mark.e2e_om_ops_manager_recovery
class TestOpsManagerCreation(OpsManagerBase):
    '''
    name: Ops Manager broken initialization
    description: |
      1. Submit Ops Manager creation
      2. Soon after the lightweight server starts and admin is created - the test removes the api key
      3. AppDB doesn't reconcile because of wrong version - so the lightweight server is still running
      4. The reconciliation retries, the check for host:8080 passes
      5. The Operator realizes that OM is in lightweight mode, tries to create the user (not created as already exists)
      6. The Operator checks the admin key - it doesn't exist, this is an unrecoverable situation, deletes the sts and restarts
      7. The test fixes appdb version
      8. The Operator creates OM statefulset again, the first user is created, saved to api key
      9. AppDB reconciles and everything ends successfully

      Disclaimer: the logic is quite complicated as we cannot just "break" API server and "fail" api key creation, so
      we have to emulate the failed scenario more complicated way.
    '''

    @classmethod
    def setup_env(cls):
        resource = yaml.safe_load(open(fixture("om_ops_manager_recovery.yaml")))
        om_cr = cls.om_cr_from_resource(resource)
        cls.create_custom_resource_from_object(cls.get_namespace(), resource)

        # starting the thread which will track the api secret and remove it once it's created
        # (only once)
        remove_func = lambda : cls.remove_api_secret(om_cr)
        spoiler_thread = threading.Thread(target=remove_func)
        spoiler_thread.start()

        cls.wait_until('om_in_error_state', 400)

        om_cr = cls.read_om_cr()
        assert "failed to read the admin key secret" in om_cr.get_om_status()['message']
        assert "Failed to create/update replica set" in om_cr.get_appdb_status()['message']

        print("{} got to failed state - this is expected!".format(om_cr.name()))

    def test_recovery(self):
        """ Note, that after the api key is removed and OM is stuck in lightweight mode - the Operator is expected
        to indicate this and remove the statefulset so the api key will be generated again """

        # "fixing" appdb
        resource = yaml.safe_load(open(fixture("om_ops_manager_recovery.yaml")))
        self.patch_custom_resource_from_object(self.get_namespace(),
                                               resource,
                                               '[{"op":"replace","path":"/spec/applicationDatabase/version",'
                                               '"value":"4.0.0"}]')

        om_cr = self.om_cr_from_resource(resource)
        print("The appdb version got fixed for {} - now it's expected to start".format(om_cr.name()))
        self.wait_until('om_in_running_state', 1000)

        # as OM has reached the goal - we can initialize the OM context now
        self.init_om_context(om_cr)

    @skip_if_local
    def test_om(self):
        OMTester(self.om_context).assert_healthiness()

    @classmethod
    def remove_api_secret(cls, om_cr: OpsManagerCR):
        while True:
            try:
                cls.clients("corev1").read_namespaced_secret(om_cr.api_key_secret(), cls.get_namespace())
            except ApiException:
                time.sleep(2)
                continue
            print("Found the secret {}/{}, removing it".format(cls.get_namespace(), om_cr.api_key_secret()))
            cls.clients("corev1").delete_namespaced_secret(om_cr.api_key_secret(), cls.get_namespace())
            break
