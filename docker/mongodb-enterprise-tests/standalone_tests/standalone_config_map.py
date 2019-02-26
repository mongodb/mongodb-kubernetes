import time

from kubernetes.client import V1ConfigMap
from kubetester import KubernetesTester


class TestStandaloneListensConfigMap(KubernetesTester):
    '''
    name: Standalone tracks configmap changes
    description: |
      Creates a standalone, then changes configmap (renames the project) and checks that the reconciliation for the |
      standalone happened
    create:
      file: fixtures/standalone.yaml
      wait_until: in_running_state
      timeout: 60
    '''

    def test_patch_config_map(self):
        projectName = KubernetesTester.random_k8s_name()
        config_map = V1ConfigMap(data={"projectName": projectName})
        self.clients("corev1").patch_namespaced_config_map("my-project", self.get_namespace(), config_map)

        print('Patched the ConfigMap - changed group name to "{}"'.format(projectName))

        # Sleeping for short to make sure the standalone has gone to Pending state
        time.sleep(5)
        assert KubernetesTester._check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Pending"
        )
        KubernetesTester.wait_until('in_running_state', 120)

        # Checking that the new group was created in OM
        new_group_id = KubernetesTester.query_group(projectName)["id"]
        assert new_group_id is not None

        KubernetesTester.remove_group(new_group_id)
