from operator import attrgetter

import pytest
from kubetester.kubetester import skip_if_local
from kubetester.omtester import OMTester
from tests.opsmanager.om_base import OpsManagerBase

"""
Current test focuses on backup capabilities
Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
updates in one test 
"""


@pytest.mark.e2e_om_ops_manager_backup
class TestOpsManagerCreation(OpsManagerBase):
    """
    name: Ops Manager successful creation with backup enabled
    description: |
      Creates an Ops Manager instance with backup enabled.
    create:
      file: om_ops_manager_backup.yaml
      wait_until: om_in_running_state
      timeout: 900
    """

    def test_daemon_statefulset(self):
        statefulset = self.appsv1.read_namespaced_stateful_set_status(self.om_cr.backup_sts_name(), self.namespace)
        assert statefulset.status.ready_replicas == 1
        assert statefulset.status.current_replicas == 1

        # pod template has volume mount request
        assert ("/head", "head") in \
               ((mount.mount_path, mount.name) for mount in statefulset.spec.template.spec.containers[0].volume_mounts)

    def test_daemon_pvc(self):
        """ Verifies the PVCs mounted to the pod """
        pod = self.corev1.read_namespaced_pod(self.om_cr.backup_pod_name(), self.namespace)
        claims = [volume for volume in pod.spec.volumes if getattr(volume, "persistent_volume_claim")]
        assert len(claims) == 1
        claims.sort(key=attrgetter('name'))

        self.check_single_pvc(claims[0], "head", self.om_cr.backup_head_pvc_name(), "500M", "gp2")

    def test_no_daemon_service_created(self):
        """ Backup daemon serves no incoming traffic so no service must be created """
        services = self.corev1.list_namespaced_service(self.namespace).items

        # 1 for AppDB and 2 for Ops Manager statefulset
        assert len(services) == 3

    @skip_if_local
    def test_om(self):
        OMTester(self.om_context).assert_healthiness()
