import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator

MDB_RESOURCE = "test-no-tls-no-status"


@pytest.mark.e2e_standalone_no_tls_no_status_is_set
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_standalone_no_tls_no_status_is_set
class TestStandaloneWithNoTLS(KubernetesTester):
    """
    name: Standalone with no TLS should not have empty "additionalMongodConfig" attribute set.
    """

    def test_create_standalone(self, custom_mdb_version: str):
        resource = MongoDB.from_yaml(load_fixture("test-no-tls-no-status.yaml"), namespace=self.namespace)
        resource.set_version(custom_mdb_version)
        resource.update()
        resource.assert_reaches_phase(Phase.Running)

    def test_mdb_resource_status_is_correct(self):
        mdb = self.customv1.get_namespaced_custom_object("mongodb.com", "v1", self.namespace, "mongodb", MDB_RESOURCE)

        assert mdb["status"]["phase"] == "Running"
        assert "additionalMongodConfig" not in mdb["spec"]
        assert "security" not in mdb


@pytest.mark.e2e_standalone_no_tls_no_status_is_set
class TestStandaloneWithNoTLSDeletion(KubernetesTester):
    """
    name: Standalone with no TLS Status should be removed
    delete:
      file: test-no-tls-no-status.yaml
      wait_until: mongo_resource_deleted
    """

    def test_deletion(self):
        assert True
