from operator import attrgetter

import pytest
from kubetester import get_default_storage_class
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase


@pytest.mark.e2e_replica_set_pv_multiple
class TestCreateStorageClass(KubernetesTester):
    """
    description: |
      Creates a gp2 storage class if it does not exist already.
      This is required as it seems that this storage class exists in
      Kops and Openshift, but not on kind. This type of StorageClass is
      based on the rancher.io/local-path provider, so it only works
      on Kind.
    """

    def test_setup_gp2_storage_class(self):
        KubernetesTester.make_default_gp2_storage_class()


@pytest.mark.e2e_replica_set_pv_multiple
class TestReplicaSetMultiplePersistentVolumeCreation(KubernetesTester):

    RESOURCE_NAME = "rs001-pv-multiple"
    custom_labels = {"label1": "val1", "label2": "val2"}

    def test_create_replicaset(self, custom_mdb_version: str):
        resource = MongoDB.from_yaml(load_fixture("replica-set-pv-multiple.yaml"), namespace=self.namespace)
        resource.set_version(custom_mdb_version)
        resource.update()

        resource.assert_reaches_phase(Phase.Running)

    def test_sts_creation(self):
        sts = self.appsv1.read_namespaced_stateful_set(self.RESOURCE_NAME, self.namespace)

        assert sts.api_version == "apps/v1"
        assert sts.kind == "StatefulSet"
        assert sts.status.current_replicas == 2
        assert sts.status.ready_replicas == 2
        sts_labels = sts.metadata.labels
        for k in self.custom_labels:
            assert k in sts_labels and sts_labels[k] == self.custom_labels[k]

    def test_pvc_are_created_and_bound(self):
        """3 mount points must be mounted to 3 pvc."""
        for idx, podname in enumerate(self._get_pods(self.RESOURCE_NAME + "-{}", 2)):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            self.check_pvc_for_pod(idx, pod)

    def check_pvc_for_pod(self, idx, pod):
        claims = [volume for volume in pod.spec.volumes if getattr(volume, "persistent_volume_claim")]
        assert len(claims) == 3

        claims.sort(key=attrgetter("name"))

        default_sc = get_default_storage_class()
        KubernetesTester.check_single_pvc(
            self.namespace,
            claims[0],
            "data",
            "data-{}-{}".format(self.RESOURCE_NAME, idx),
            "2Gi",
            "gp2",
            self.custom_labels,
        )

        # Note that PVC gets the default storage class for cluster even if it wasn't requested initially
        KubernetesTester.check_single_pvc(
            self.namespace,
            claims[1],
            "journal",
            f"journal-{self.RESOURCE_NAME}-{idx}",
            "1Gi",
            default_sc,
            self.custom_labels,
        )
        KubernetesTester.check_single_pvc(
            self.namespace,
            claims[2],
            "logs",
            f"logs-{self.RESOURCE_NAME}-{idx}",
            "1G",
            default_sc,
            self.custom_labels,
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
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_pvc_are_bound(self):
        "Should check the used PVC are still there in the Bound status."
        all_claims = self.corev1.list_namespaced_persistent_volume_claim(self.namespace)
        assert len(all_claims.items) == 6

        for claim in all_claims.items:
            assert claim.status.phase == "Bound"
