import pytest
from kubetester.kubetester import skip_if_local
from kubetester.omtester import OMTester

from tests.opsmanager.om_base import OpsManagerBase

"""
The test for the latest OM image. This test is supposed to be updated each time new OM is released.
Don't forget to set 'testing:true' in 'release.json' file for a matching OM version!
"""


@pytest.mark.e2e_om_ops_manager_latest
class TestOpsManagerCreation(OpsManagerBase):
    """
    name: Latest Ops Manager successful creation
    description: |
      Creates an Ops Manager instance (latest version) with AppDB of size 3.
    create:
      file: om_ops_manager_basic.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.2.6"}]'
      wait_until: om_in_running_state
      timeout: 900
    """

    @skip_if_local
    def test_om(self):
        """Checks that the OM is responsive and test service is available (enabled by 'mms.testUtil.enabled')."""
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()
        om_tester.assert_version("4.2.6")

    @skip_if_local
    def test_appdb(self):
        mdb_tester = self.om_cr.get_appdb_mongo_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version("4.2.0")
