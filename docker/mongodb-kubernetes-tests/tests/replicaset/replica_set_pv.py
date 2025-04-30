import time

import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from tests.conftest import LEGACY_OPERATOR_NAME, OPERATOR_NAME


@pytest.mark.e2e_replica_set_pv
class TestReplicaSetPersistentVolumeCreation(KubernetesTester):
    def test_create_replicaset(self, custom_mdb_version: str):
        resource = MongoDB.from_yaml(load_fixture("replica-set-pv.yaml"), namespace=self.namespace)
        resource.set_version(custom_mdb_version)
        resource.update()

        resource.assert_reaches_phase(Phase.Running)

    def test_replica_set_sts_exists(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-pv", self.namespace)
        assert sts

    def test_sts_creation(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-pv", self.namespace)

        assert sts.api_version == "apps/v1"
        assert sts.kind == "StatefulSet"
        assert sts.status.current_replicas == 3
        assert sts.status.ready_replicas == 3

    def test_sts_metadata(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-pv", self.namespace)

        assert sts.metadata.name == "rs001-pv"
        assert sts.metadata.labels["app"] == "rs001-pv-svc"
        assert sts.metadata.namespace == self.namespace
        owner_ref0 = sts.metadata.owner_references[0]
        assert owner_ref0.api_version == "mongodb.com/v1"
        assert owner_ref0.kind == "MongoDB"
        assert owner_ref0.name == "rs001-pv"

    def test_sts_replicas(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-pv", self.namespace)
        assert sts.spec.replicas == 3

    def test_sts_template(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-pv", self.namespace)

        tmpl = sts.spec.template
        assert tmpl.metadata.labels["app"] == "rs001-pv-svc"
        assert tmpl.metadata.labels["controller"] == LEGACY_OPERATOR_NAME
        assert tmpl.spec.affinity.node_affinity is None
        assert tmpl.spec.affinity.pod_affinity is None
        assert tmpl.spec.affinity.pod_anti_affinity is not None

    def _get_pods(self, podname, qty=3):
        return [podname.format(i) for i in range(qty)]

    def test_pvc_are_created_and_bound(self):
        "PersistentVolumeClaims should be created with the correct attributes."
        bound_pvc_names = []
        for podname in self._get_pods("rs001-pv-{}", 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            bound_pvc_names.append(pod.spec.volumes[0].persistent_volume_claim.claim_name)

        for pvc_name in bound_pvc_names:
            pvc_status = self.corev1.read_namespaced_persistent_volume_claim_status(pvc_name, self.namespace)
            assert pvc_status.status.phase == "Bound"

    def test_om_processes(self, custom_mdb_version: str):
        config = self.get_automation_config()
        processes = config["processes"]
        for idx, p in enumerate(processes):
            assert custom_mdb_version in p["version"]
            assert p["name"] == f"rs001-pv-{idx}"
            assert p["processType"] == "mongod"
            assert p["authSchemaVersion"] == 5
            assert p["featureCompatibilityVersion"] == fcv_from_version(custom_mdb_version)
            assert p["hostname"] == "rs001-pv-" + f"{idx}" + ".rs001-pv-svc.{}.svc.cluster.local".format(self.namespace)
            assert p["args2_6"]["net"]["port"] == 27017
            assert p["args2_6"]["replication"]["replSetName"] == "rs001-pv"
            assert p["args2_6"]["storage"]["dbPath"] == "/data"
            assert p["args2_6"]["systemLog"]["destination"] == "file"
            assert p["args2_6"]["systemLog"]["path"] == "/var/log/mongodb-mms-automation/mongodb.log"
            assert p["logRotate"]["sizeThresholdMB"] == 1000
            assert p["logRotate"]["timeThresholdHrs"] == 24

    def test_replica_set_was_configured(self):
        "Should connect to one of the mongods and check the replica set was correctly configured."
        hosts = ["rs001-pv-{}.rs001-pv-svc.{}.svc.cluster.local:27017".format(i, self.namespace) for i in range(3)]

        primary, secondaries = self.wait_for_rs_is_ready(hosts)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.e2e_replica_set_pv
class TestReplicaSetPersistentVolumeDelete(KubernetesTester):
    """
    name: Replica Set Deletion
    tags: replica-set, persistent-volumes, removal
    description: |
      Deletes a Replica Set.
    delete:
      file: replica-set-pv.yaml
      wait_until: mongo_resource_deleted
      timeout: 200
    """

    def test_replica_set_sts_doesnt_exist(self):
        """The StatefulSet must be removed by Kubernetes as soon as the MongoDB resource is removed.
        Note, that this may lag sometimes (caching or whatever?) and it's more safe to wait a bit"""
        time.sleep(15)
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set("rs001-pv", self.namespace)

    def test_service_does_not_exist(self):
        "Services should not exist"
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service("rs001-pv-svc", self.namespace)

    def test_pvc_are_unbound(self):
        "Should check the used PVC are still there in the expected status."
        pass
