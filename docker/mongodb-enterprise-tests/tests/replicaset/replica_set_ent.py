import pytest

from kubetester.kubetester import KubernetesTester
from kubernetes import client


@pytest.mark.e2e_replica_set_ent
class TestReplicaSetEnterpriseCreation(KubernetesTester):
    """
    name: Replica Set Creation with Mongo Enterprise Edition
    tags: replica-set, enterprise, creation
    description: |
      Creates a Replica Set with Mongo Enterprise Edition
    create:
      file: replica-set-ent.yaml
      wait_until: in_running_state
      timeout: 180
    """

    def test_replica_set_sts_exists(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-ent", self.namespace)
        assert sts

    def test_sts_creation(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-ent", self.namespace)

        assert sts.api_version == "apps/v1"
        assert sts.kind == "StatefulSet"
        assert sts.status.current_replicas == 3
        assert sts.status.ready_replicas == 3

    def test_sts_metadata(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-ent", self.namespace)

        assert sts.metadata.name == "rs001-ent"
        assert sts.metadata.labels["app"] == "rs001-ent-svc"
        assert sts.metadata.namespace == self.namespace
        owner_ref0 = sts.metadata.owner_references[0]
        assert owner_ref0.api_version == "mongodb.com/v1"
        assert owner_ref0.kind == "MongoDB"
        assert owner_ref0.name == "rs001-ent"

    def test_sts_replicas(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-ent", self.namespace)
        assert sts.spec.replicas == 3

    def test_sts_template(self):
        sts = self.appsv1.read_namespaced_stateful_set("rs001-ent", self.namespace)

        tmpl = sts.spec.template
        assert tmpl.metadata.labels["app"] == "rs001-ent-svc"
        assert tmpl.metadata.labels["controller"] == "mongodb-enterprise-operator"
        assert tmpl.spec.affinity.node_affinity is None
        assert tmpl.spec.affinity.pod_affinity is None
        assert tmpl.spec.affinity.pod_anti_affinity is not None

    def test_replica_set_was_configured(self):
        'Should connect to one of the mongods and check the replica set was correctly configured.'
        hosts = [
            "rs001-ent-{}.rs001-ent-svc.{}.svc.cluster.local:27017".format(
                i, self.namespace
            )
            for i in range(3)
        ]

        primary, secondaries = self.wait_for_rs_is_ready(hosts)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.e2e_replica_set_ent
class TestReplicaSetEnterpriseDelete(KubernetesTester):
    """
    name: Replica Set
    tags: replica-set, enterprise, removal
    description: |
      Deletes a Replica Set.
    delete:
      file: replica-set-ent.yaml
      wait_until: mongo_resource_deleted
      timeout: 120
    """
    def test_om_state_deleted(self):
        KubernetesTester.check_om_state_cleaned()

    def test_replica_set_sts_doesnt_exist(self):
        "StatefulSet should not exist"
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set("rs001-ent", self.namespace)

    def test_service_does_not_exist(self):
        "Services should not exist"
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service("rs001-ent-svc", self.namespace)
