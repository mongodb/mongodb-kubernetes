import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.omtester import skip_if_cloud_manager

mdb_resource = "test-no-tls-no-status"


@pytest.mark.e2e_standalone_no_tls_no_status_is_set
class TestStandaloneWithNoTLS(KubernetesTester):
    """
    name: Standalone with no TLS should not have empty "additionalMongodConfig" attribute set.
    create:
      file: test-no-tls-no-status.yaml
      wait_until: in_running_state
      timeout: 180
    """

    def test_mdb_resource_status_is_correct(self):
        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_resource
        )

        assert mdb["status"]["phase"] == "Running"
        assert "additionalMongodConfig" not in mdb["status"]
        assert "net" not in mdb


# TODO: Find the reason behind this error.
# Details in here -> https://jira.mongodb.org/browse/CLOUDP-43066
@skip_if_cloud_manager
@pytest.mark.e2e_standalone_no_tls_no_status_is_set
class TestStandaloneWithNoTLSDeletion(KubernetesTester):
    """
    name: Standalone with no TLS Status should be removed
    delete:
      file: test-no-tls-no-status.yaml
      wait_until: mongo_resource_deleted
      timeout: 180
    """

    def test_deletion(self):
        assert True
