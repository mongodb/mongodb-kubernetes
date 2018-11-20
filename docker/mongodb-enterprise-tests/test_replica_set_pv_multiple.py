import pytest

from kubetester import KubernetesTester


@pytest.mark.replica_set_pv_multiple
class TestReplicaSetMultiplePersistentVolumeCreation(KubernetesTester):
    """
    name: Replica Set Creation with Multiple PersistentVolumes
    tags: replica-set, persistent-volumes, creation
    description: |
      Creates a Replica Set with multiple persistent volumes (one per each mount point)
    create:
      file: fixtures/replica-set-pv-multiple.yaml
      wait_until: sts/rs001-pv-multiple -> status.ready_replicas == 2
      wait_for: 20
    """
    RESOURCE_NAME = "rs001-pv-multiple"

    def test_sts_creation(self):
        sts = self.appsv1.read_namespaced_stateful_set(self.RESOURCE_NAME, self.namespace)

        assert sts.api_version == "apps/v1"
        assert sts.kind == "StatefulSet"
        assert sts.status.current_replicas == 2
        assert sts.status.ready_replicas == 2

    def test_pvc_are_created_and_bound(self):
        """3 mount points must be mounted to 3 pvc."""
        for idx, podname in enumerate(self._get_pods(self.RESOURCE_NAME + '-{}', 2)):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            self.check_pvc_for_pod(idx, pod)

    def check_pvc_for_pod(self, idx, pod):
        claims = list(filter(lambda v: getattr(v, "persistent_volume_claim") is not None, pod.spec.volumes))
        assert len(claims) == 3
        claims.sort(key=lambda claim: claim.name)
        self.check_single_pvc(claims[0], "data", 'data-{}-{}'.format(self.RESOURCE_NAME, idx), "2Gi", "gp2")

        # Note that PVC gets the default storage class for cluster even if it wasn't requested initially
        self.check_single_pvc(claims[1], "journal", 'journal-{}-{}'.format(self.RESOURCE_NAME, idx), "1Gi", "gp2")
        self.check_single_pvc(claims[2], "logs", 'logs-{}-{}'.format(self.RESOURCE_NAME, idx), "1G", "gp2")

    def check_single_pvc(self, volume, expected_name, expected_claim_name, expected_size,
                         storage_class=None):
        assert volume.name == expected_name
        assert volume.persistent_volume_claim.claim_name == expected_claim_name

        pvc = self.corev1.read_namespaced_persistent_volume_claim(expected_claim_name, self.namespace)
        assert pvc.status.phase == 'Bound'
        assert pvc.spec.resources.requests["storage"] == expected_size
        if storage_class is not None:
            assert pvc.spec.storage_class_name == storage_class
        else:
            assert getattr(pvc.spec, "storage_class_name") is None


@pytest.mark.replica_set_pv_multiple
class TestReplicaSetMultiplePersistentVolumeDelete(KubernetesTester):
    """
    name: Replica Set Deletion
    tags: replica-set, persistent-volumes, removal
    description: |
      Deletes a Replica Set.
    delete:
      file: fixtures/replica-set-pv-multiple.yaml
      wait_for: 90
    """

    def test_pvc_are_unbound(self):
        "Should check the used PVC are still there in the Bound status."
        all_claims = self.corev1.list_namespaced_persistent_volume_claim(self.namespace)
        assert len(all_claims.items) == 6
        for claim in all_claims.items:
            assert claim.status.phase == 'Bound'
