from kubetester.kubetester import KubernetesTester


class TestReplicaSetExposedExternally(KubernetesTester):
    '''
    name: Replica set exposed externally
    description: |
        Tests that a replica set can be appropriately exposed externally.
    create:
      file: fixtures/replica-set-externally-exposed.yaml
      wait_until: in_running_state
      timeout: 60
    '''

    def test_nodeport_service_exists(self):
        services = self.clients("corev1").list_namespaced_service(self.get_namespace())
        assert len(services.items) == 2
        assert len([s for s in services.items if s.spec.type == "NodePort"]) == 1
