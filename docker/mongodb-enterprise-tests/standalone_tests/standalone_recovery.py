import yaml
from kubernetes.client import V1ConfigMap

from kubetester import KubernetesTester


class TestStandaloneRecoversBadOmConfiguration(KubernetesTester):
    '''
    name: Standalone broken OM connection
    description: |
      Creates a standalone with a bad OM connection (ConfigMap is broken) and ensures it enters a failed state |
      Then the config map is fixed and the standalone is expected to reach good state eventually
    '''
    @classmethod
    def setup_env(cls):
        config_map = V1ConfigMap(data={"baseUrl": "http://foo.bar"})
        cls.clients("corev1").patch_namespaced_config_map("my-project", cls.get_namespace(), config_map)

        with open("fixtures/standalone.yaml") as f:
            resource = yaml.safe_load(f)

        cls.create_custom_resource_from_object(cls.get_namespace(), resource)

        KubernetesTester.wait_until('in_error_state', 20)
        mrs = KubernetesTester.get_resource()
        assert "Failed to prepare Ops Manager connection" in mrs['status']['message']

    def test_recovery(self):
        config_map = V1ConfigMap(data={"baseUrl": KubernetesTester.get_om_base_url()})
        KubernetesTester.clients("corev1").patch_namespaced_config_map("my-project", KubernetesTester.get_namespace(), config_map)

        KubernetesTester.wait_until('in_running_state', 80)
