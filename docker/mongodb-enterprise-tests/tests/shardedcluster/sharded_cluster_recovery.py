import pytest

import yaml
from kubernetes.client import V1Secret

from kubetester.kubetester import KubernetesTester, fixture


@pytest.mark.e2e_sharded_cluster_recovery
class TestShardedClusterRecoversBadOmConfiguration(KubernetesTester):
    """
    name: Sharded cluster broken OM connection
    description: |
      Creates a sharded cluster with a bad OM connection (public key is broken) and ensures it enters a failed state |
      Then the secret is fixed and the standalone is expected to reach good state eventually
    """

    @classmethod
    def setup_env(cls):
        secret = V1Secret(string_data={"publicApiKey": "wrongKey"})
        cls.clients("corev1").patch_namespaced_secret("my-credentials", cls.get_namespace(), secret)

        resource = yaml.safe_load(open(fixture("sharded-cluster-single.yaml")))

        cls.create_custom_resource_from_object(cls.get_namespace(), resource)

        KubernetesTester.wait_until("in_error_state", 20)

        mrs = KubernetesTester.get_resource()
        assert "You are not authorized for this resource" in mrs["status"]["message"]

    def test_recovery(self):
        secret = V1Secret(string_data={"publicApiKey": self.get_om_api_key()})
        self.clients("corev1").patch_namespaced_secret("my-credentials", self.get_namespace(), secret)

        KubernetesTester.wait_until("in_running_state")
