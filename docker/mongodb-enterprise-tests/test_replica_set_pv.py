import pytest

from kubetester import KubernetesTester
from kubernetes import client


@pytest.mark.replica_set_pv
class TestReplicaSetPersistentVolumeCreation(KubernetesTester):
    """
    name: Replica Set Creation with PersistentVolumes
    tags: replica-set, persistent-volumes, creation
    description: |
      Creates a Replica Set and allocates a PersistentVolume to it.
    create:
      file: fixtures/replica-set-pv.yaml
      wait_until: sts/rs001-pv -> status.ready_replicas == 3
      wait_for: 30
    """

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
        assert owner_ref0.kind == "MongoDbReplicaSet"
        assert owner_ref0.name == "rs001-pv"

    def test_sts_replicas(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-pv", self.namespace)
        assert sts.spec.replicas == 3

    def test_sts_template(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-pv", self.namespace)

        tmpl = sts.spec.template
        assert tmpl.metadata.labels["app"] == "rs001-pv-svc"
        assert tmpl.metadata.labels["controller"] == "mongodb-enterprise-operator"
        assert tmpl.spec.affinity.node_affinity is None
        assert tmpl.spec.affinity.pod_affinity is None
        assert tmpl.spec.affinity.pod_anti_affinity is not None

    def _get_pods(self, podname, qty=3):
        return [podname.format(i) for i in range(qty)]

    def test_pvc_are_created_and_bound(self):
        "PersistentVolumeClaims should be created with the correct attributes."
        bound_pvc_names = []
        for podname in self._get_pods('rs001-pv-{}', 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            bound_pvc_names.append(
                pod.spec.volumes[0].persistent_volume_claim.claim_name
            )

        for pvc_name in bound_pvc_names:
            pvc_status = self.corev1.read_namespaced_persistent_volume_claim_status(
                pvc_name, self.namespace
            )
            assert pvc_status.status.phase == 'Bound'

    def test_om_processes(self):
        config = self.get_automation_config()
        processes = config["processes"]
        p0 = processes[0]
        p1 = processes[1]
        p2 = processes[2]

        # First Process
        assert p0["name"] == "rs001-pv-0"
        assert p0["processType"] == "mongod"
        assert p0["version"] == "4.0.1"
        assert p0["authSchemaVersion"] == 5
        assert p0["featureCompatibilityVersion"] == "4.0"
        assert p0["hostname"] == "rs001-pv-0.rs001-pv-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p0["args2_6"]["net"]["port"] == 27017
        assert p0["args2_6"]["replication"]["replSetName"] == "rs001-pv"
        assert p0["args2_6"]["storage"]["dbPath"] == "/data"
        assert p0["args2_6"]["systemLog"]["destination"] == "file"
        assert p0["args2_6"]["systemLog"]["path"] == "/var/log/mongodb-mms-automation/mongodb.log"
        assert p0["logRotate"]["sizeThresholdMB"] == 1000
        assert p0["logRotate"]["timeThresholdHrs"] == 24

        # Second Process
        assert p1["name"] == "rs001-pv-1"
        assert p1["processType"] == "mongod"
        assert p1["version"] == "4.0.1"
        assert p1["authSchemaVersion"] == 5
        assert p1["featureCompatibilityVersion"] == "4.0"
        assert p1["hostname"] == "rs001-pv-1.rs001-pv-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p1["args2_6"]["net"]["port"] == 27017
        assert p1["args2_6"]["replication"]["replSetName"] == "rs001-pv"
        assert p1["args2_6"]["storage"]["dbPath"] == "/data"
        assert p1["args2_6"]["systemLog"]["destination"] == "file"
        assert p1["args2_6"]["systemLog"]["path"] == "/var/log/mongodb-mms-automation/mongodb.log"
        assert p1["logRotate"]["sizeThresholdMB"] == 1000
        assert p1["logRotate"]["timeThresholdHrs"] == 24

        # Third Process
        assert p2["name"] == "rs001-pv-2"
        assert p2["processType"] == "mongod"
        assert p2["version"] == "4.0.1"
        assert p2["authSchemaVersion"] == 5
        assert p2["featureCompatibilityVersion"] == "4.0"
        assert p2["hostname"] == "rs001-pv-2.rs001-pv-svc.{}.svc.cluster.local".format(
            self.namespace
        )
        assert p2["args2_6"]["net"]["port"] == 27017
        assert p2["args2_6"]["replication"]["replSetName"] == "rs001-pv"
        assert p2["args2_6"]["storage"]["dbPath"] == "/data"
        assert p2["args2_6"]["systemLog"]["destination"] == "file"
        assert p2["args2_6"]["systemLog"]["path"] == "/var/log/mongodb-mms-automation/mongodb.log"
        assert p2["logRotate"]["sizeThresholdMB"] == 1000
        assert p2["logRotate"]["timeThresholdHrs"] == 24

    def test_replica_set_was_configured(self):
        'Should connect to one of the mongods and check the replica set was correctly configured.'
        hosts = [
            "rs001-pv-{}.rs001-pv-svc.{}.svc.cluster.local:27017".format(
                i, self.namespace
            )
            for i in range(3)
        ]

        primary, secondaries = self.wait_for_rs_is_ready(hosts)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.replica_set_pv
class TestReplicaSetPersistentVolumeDelete(KubernetesTester):
    """
    name: Replica Set Deletion
    tags: replica-set, persistent-volumes, removal
    description: |
      Deletes a Replica Set.
    delete:
      file: fixtures/replica-set-pv.yaml
      wait_for: 90
    """

    def test_replica_set_sts_doesnt_exist(self):
        "StatefulSet should not exist"
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set("rs001-pv", self.namespace)

    def test_service_does_not_exist(self):
        "Services should not exist"
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service("rs001-pv-svc", self.namespace)

    def test_pvc_are_unbound(self):
        "Should check the used PVC are still there in the expected status."
        pass

    def test_om_replica_set_is_deleted(self):
        config = self.get_automation_config()
        assert len(config["replicaSets"]) == 0

    def test_om_processes_are_deleted(self):
        config = self.get_automation_config()
        assert len(config["processes"]) == 0
