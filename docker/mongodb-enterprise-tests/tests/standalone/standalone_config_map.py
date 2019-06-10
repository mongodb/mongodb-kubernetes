import time
import pytest

from kubernetes.client import V1ConfigMap
from kubernetes import client
from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_standalone_config_map
class TestStandaloneListensConfigMap(KubernetesTester):
    '''
    name: Standalone tracks configmap changes
    description: |
      Creates a standalone, then changes configmap (renames the project) and checks that the reconciliation for the |
      standalone happened
    create:
      file: standalone.yaml
      wait_until: in_running_state
      timeout: 120
    '''

    def test_patch_config_map(self):
        projectName = KubernetesTester.random_k8s_name()
        config_map = V1ConfigMap(data={"projectName": projectName})
        self.clients("corev1").patch_namespaced_config_map("my-project", self.get_namespace(), config_map)

        print('Patched the ConfigMap - changed group name to "{}"'.format(projectName))

        # Sleeping for short to make sure the standalone has gone to Pending state
        time.sleep(5)
        assert KubernetesTester.check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Pending"
        )
        KubernetesTester.wait_until('in_running_state', 120)

        # Checking that the new group was created in OM
        orgid = KubernetesTester.get_om_org_id()
        new_group_id = KubernetesTester.query_group(projectName, orgid)["id"]
        assert new_group_id is not None

        KubernetesTester.remove_group(new_group_id)


@pytest.mark.e2e_standalone_config_map
class TestStandaloneListensConfigMapDelete(KubernetesTester):
    '''
    name: Standalone MDB resource is removed
    delete:
      file: standalone.yaml
      wait_for: 120
    '''
    def test_replica_set_sts_doesnt_exist(self):
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set('standalone', self.namespace)
