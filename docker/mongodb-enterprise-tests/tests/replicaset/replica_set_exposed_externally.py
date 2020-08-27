import pytest
from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_replica_set_exposed_externally
class TestReplicaSetExposedExternally(KubernetesTester):
    """
    name: Replica set exposed externally
    description: |
        Tests that a replica set can be appropriately exposed externally.
    create:
      file: replica-set-externally-exposed.yaml
      wait_until: in_running_state
      timeout: 60
    """

    def test_nodeport_service_exists(self):
        global node_port
        service = self.clients("corev1").read_namespaced_service(
            "my-replica-set-externally-exposed-svc-external", self.get_namespace()
        )
        assert service.spec.type == "NodePort"
        assert service.spec.ports[0].port == 27017
        assert service.spec.ports[0].node_port
        node_port = service.spec.ports[0].node_port


@pytest.mark.e2e_replica_set_exposed_externally
class TestReplicaSetExposedExternallyUpdate(KubernetesTester):
    """
    name: Replica set exposed externally
    description: |
        Tests that a NodePort service preserves its node port after replica set update
    update:
      file: replica-set-externally-exposed.yaml
      patch: '[{"op":"replace","path":"/spec/members", "value": 2}]'
      wait_until: in_running_state
      timeout: 150
    """

    def test_nodeport_service_node_port_stays_the_same(self):
        service = self.clients("corev1").read_namespaced_service(
            "my-replica-set-externally-exposed-svc-external", self.get_namespace()
        )
        assert service.spec.type == "NodePort"
        assert service.spec.ports[0].node_port == node_port
