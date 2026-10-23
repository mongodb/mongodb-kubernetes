import pytest
from kubetester.kubetester import KubernetesTester, fixture
from kubetester.mongodb import MongoDB


@pytest.mark.e2e_replica_set_recovery
class TestReplicaSetBadStateCreation(KubernetesTester):
    """
    name: Replica Set Bad State Creation
    tags: replica-set, creation
    description: |
      Creates a Replica set with a bad configuration (wrong credentials) and ensures it enters a failed state
    """

    @classmethod
    def setup_env(cls):
        resource = MongoDB.from_yaml(fixture("replica-set-invalid.yaml"), namespace=cls.get_namespace())
        cls.create_mongodb_from_object(cls.get_namespace(), resource.backing_obj)
        cls.wait_until("in_error_state", 180)

    def test_in_error_state(self):
        mrs = KubernetesTester.get_resource()
        assert mrs["status"]["phase"] == "Failed"


@pytest.mark.e2e_replica_set_recovery
class TestReplicaSetRecoversFromBadState(KubernetesTester):
    """
    name: Replica Set Bad State Recovery
    tags: replica-set, creation
    description: |
      Updates spec of replica set in a bad state and ensures it is updated to the running state correctly
    """

    @classmethod
    def setup_env(cls):
        resource = MongoDB.from_yaml(fixture("replica-set-invalid.yaml"), namespace=cls.get_namespace())
        patch = [{"op": "replace", "path": "/spec/credentials", "value": "my-credentials"}]
        cls.patch_custom_resource_from_object(cls.get_namespace(), resource.backing_obj, patch)
        cls.wait_until("in_running_state", 400)

    def test_in_running_state(self):
        mrs = KubernetesTester.get_resource()
        assert mrs["status"]["phase"] == "Running"
