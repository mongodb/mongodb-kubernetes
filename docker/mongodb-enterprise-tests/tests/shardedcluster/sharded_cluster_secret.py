import pytest

from kubernetes.client import V1Secret
from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_sharded_cluster_secret
class TestShardedClusterListensSecret(KubernetesTester):
    '''
    name: ShardedCluster tracks configmap changes
    description: |
      Creates a sharded cluster, then changes secret - breaks the api key and checks that the reconciliation for the |
      standalone happened and it got into Failed state. Note, that this test cannot be run with 'make e2e .. light=true' |
      flag locally as secret must be recreated
    create:
      file: sharded-cluster-single.yaml
      wait_until: in_running_state
      timeout: 120
    '''

    def test_patch_config_map(self):
        secret = V1Secret(string_data={"publicApiKey": "wrongKey"})
        self.clients("corev1").patch_namespaced_secret("my-credentials", self.get_namespace(), secret)

        print('Patched the Secret - changed publicApiKey to "wrongKey"')

        KubernetesTester.wait_until('in_error_state', 20)
