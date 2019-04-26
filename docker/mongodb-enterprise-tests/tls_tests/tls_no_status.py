import pytest

import sys
import os
import os.path

try:
    from kubetester import KubernetesTester
except ImportError:
    # patching python import path so it finds kubetester
    sys.path.append(os.path.dirname(os.getcwd()))
    from kubetester import KubernetesTester


mdb_resource = "test-no-tls-no-status"


@pytest.mark.tls_no_status
class TestStandaloneWithNoTLS(KubernetesTester):
    """
    name: Standalone with no TLS should not have empty "additionalMongodConfig" attribute set.
    create:
      file: fixtures/test-no-tls-no-status.yaml
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


@pytest.mark.tls_no_status
class TestStandaloneWithNoTLSDeletion(KubernetesTester):
    """
    name: Standalone with no TLS Status should be removed
    delete:
      file: fixtures/test-no-tls-no-status.yaml
      wait_until: mongo_resource_deleted
      timeout: 180
    """

    def test_deletion(self):
        assert True
