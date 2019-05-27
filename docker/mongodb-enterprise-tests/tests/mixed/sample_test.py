import pytest
from kubetester.kubetester import KubernetesTester
from kubernetes import client


@pytest.mark.e2e_my_new_feature
class TestMyNewFeatureShouldPass(KubernetesTester):
    """
    name: My New Feature Test 01
    create:
      file: sample-mdb-object-to-test.yaml
      wait_until: in_running_state
    """

    def test_something_about_my_new_object(self):
        mdb_object_name = "my-mdb-object-to-test"
        mdb = client.CustomObjectsApi().get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_object_name
        )

        assert mdb["status"]["phase"] == "Running"

    def test_mdb_object_is_removed(self):
        mdb_object_name = "my-mdb-object-to-test"
        delete_opts = client.V1DeleteOptions()
        mdb = client.CustomObjectsApi().delete_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_object_name, delete_opts
        )

        assert mdb["status"] == "Success"
