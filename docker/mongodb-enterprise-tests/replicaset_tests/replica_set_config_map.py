from kubernetes.client import V1ConfigMap

from kubetester import KubernetesTester


class TestReplicaSetListensConfigMap(KubernetesTester):
    '''
    name: ReplicaSet tracks configmap changes
    description: |
      Creates a replicaSet, then changes configmap adds rubbish orgId and checks that the reconciliation for the |
      standalone happened and it got into Failed state. Note, that this test cannot be run with 'make e2e .. light=true' |
      flag locally as config map must be recreated
    create:
      file: fixtures/replica-set-single.yaml
      wait_until: in_running_state
      timeout: 60
    '''

    def test_patch_config_map(self):
        config_map = V1ConfigMap(data={"orgId": "wrongId"})
        self.clients("corev1").patch_namespaced_config_map("my-project", self.get_namespace(), config_map )

        print('Patched the ConfigMap - changed orgId to "wrongId"')

        KubernetesTester.wait_until('in_error_state', 20)

