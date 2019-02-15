import yaml
from kubernetes.client import V1Secret

from kubetester import KubernetesTester, get_crd_meta, plural


class TestShardedClusterRecoversBadOmConfiguration(KubernetesTester):
    '''
    name: Sharded cluster broken OM connection
    description: |
      Creates a sharded cluster with a bad OM connection (public key is broken) and ensures it enters a failed state |
      Then the secret is fixed and the standalone is expected to reach good state eventually
    '''
    def test_recovery(self):
        secret = V1Secret(string_data={"publicApiKey": "wrongKey"})
        self.clients("corev1").patch_namespaced_secret("my-credentials", self.get_namespace(), secret )

        resource = yaml.safe_load(open("fixtures/sharded-cluster-single.yaml"))
        # TODO change when Ben merges his changes
        name, kind, group, version = get_crd_meta(resource)
        KubernetesTester.name = name
        KubernetesTester.kind = kind
        KubernetesTester.clients("customv1").create_namespaced_custom_object(
            group, version, self.get_namespace(), plural(kind), resource
        )
        KubernetesTester.wait_until('in_error_state', 20)
        mrs = KubernetesTester.get_resource()
        assert "You are not authorized for this resource" in mrs['status']['message']

        secret = V1Secret(string_data={"publicApiKey": self.get_om_api_key()})
        self.clients("corev1").patch_namespaced_secret("my-credentials", self.get_namespace(), secret )

        KubernetesTester.wait_until('in_running_state', 200)

