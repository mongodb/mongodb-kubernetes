import yaml
from kubernetes.client import V1ConfigMap

from kubetester import KubernetesTester, get_crd_meta, plural


class TestStandaloneRecoversBadOmConfiguration(KubernetesTester):
    '''
    name: Standalone broken OM connection
    description: |
      Creates a standalone with a bad OM connection (ConfigMap is broken) and ensures it enters a failed state |
      Then the config map is fixed and the standalone is expected to reach good state eventually
    '''
    def test_recovery(self):
        config_map = V1ConfigMap(data={"baseUrl": "http://foo.bar"})
        self.clients("corev1").patch_namespaced_config_map("my-project", self.get_namespace(), config_map)

        resource = yaml.safe_load(open("fixtures/standalone.yaml"))
        # TODO change when Ben merges his changes
        name, kind, group, version = get_crd_meta(resource)
        KubernetesTester.name = name
        KubernetesTester.kind = kind
        KubernetesTester.clients("customv1").create_namespaced_custom_object(
            group, version, self.get_namespace(), plural(kind), resource
        )
        KubernetesTester.wait_until('in_error_state', 20)
        mrs = KubernetesTester.get_resource()
        assert "Failed to prepare Ops Manager connection" in mrs['status']['message']

        config_map = V1ConfigMap(data={"baseUrl": KubernetesTester.get_om_base_url()})
        KubernetesTester.clients("corev1").patch_namespaced_config_map("my-project", KubernetesTester.get_namespace(), config_map)

        KubernetesTester.wait_until('in_running_state', 80)

