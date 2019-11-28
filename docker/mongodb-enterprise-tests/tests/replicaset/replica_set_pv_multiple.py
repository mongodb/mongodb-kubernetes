import pytest

from kubetester.kubetester import KubernetesTester

from operator import attrgetter


@pytest.mark.e2e_replica_set_pv_multiple
class TestReplicaSetMultiplePersistentVolumeCreation(KubernetesTester):
    """
    name: Replica Set Creation with Multiple PersistentVolumes
    tags: replica-set, persistent-volumes, creation
    description: |
      Creates a Replica Set with multiple persistent volumes (one per each mount point)
    create:
      file: replica-set-pv-multiple.yaml
      wait_until: in_running_state
      timeout: 300
    """

    RESOURCE_NAME = "rs001-pv-multiple"

    def test_sts_creation(self):
        sts = self.appsv1.read_namespaced_stateful_set(
            self.RESOURCE_NAME, self.namespace
        )

        assert sts.api_version == "apps/v1"
        assert sts.kind == "StatefulSet"
        assert sts.status.current_replicas == 2
        assert sts.status.ready_replicas == 2

    def test_pvc_are_created_and_bound(self):
        """3 mount points must be mounted to 3 pvc."""
        for idx, podname in enumerate(self._get_pods(self.RESOURCE_NAME + "-{}", 2)):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            self.check_pvc_for_pod(idx, pod)

    def check_pvc_for_pod(self, idx, pod):
        claims = [
            volume
            for volume in pod.spec.volumes
            if getattr(volume, "persistent_volume_claim")
        ]
        assert len(claims) == 3

        claims.sort(key=attrgetter("name"))

        self.check_single_pvc(
            claims[0],
            "data",
            "data-{}-{}".format(self.RESOURCE_NAME, idx),
            "2Gi",
            "gp2",
        )

        # Note that PVC gets the default storage class for cluster even if it wasn't requested initially
        self.check_single_pvc(
            claims[1],
            "journal",
            "journal-{}-{}".format(self.RESOURCE_NAME, idx),
            "1Gi",
            "gp2",
        )
        self.check_single_pvc(
            claims[2], "logs", "logs-{}-{}".format(self.RESOURCE_NAME, idx), "1G", "gp2"
        )


@pytest.mark.e2e_replica_set_pv_multiple
class TestReplicaSetMultiplePersistentVolumeDelete(KubernetesTester):
    """
    name: Replica Set Deletion
    tags: replica-set, persistent-volumes, removal
    description: |
      Deletes a Replica Set.
    delete:
      file: replica-set-pv-multiple.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_pvc_are_bound(self):
        "Should check the used PVC are still there in the Bound status."
        all_claims = self.corev1.list_namespaced_persistent_volume_claim(self.namespace)
        assert len(all_claims.items) == 6

        for claim in all_claims.items:
            assert claim.status.phase == "Bound"
