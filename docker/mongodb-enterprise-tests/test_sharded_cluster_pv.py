import pytest

from kubetester import KubernetesTester
from kubernetes import client


@pytest.mark.sharded_cluster_pv
class TestShardedClusterCreation(KubernetesTester):
    """
    name: Sharded Cluster Creation with PV
    description: |
      Creates a simple Sharded Cluster with 1 shard, 2 mongos,
      1 replica set as config server and basic PV
    create:
      file: fixtures/sharded-cluster-pv.yaml
      wait_until: sts/sh001-pv-0 -> status.ready_replicas == 3
      wait_for: 10
    """

    def test_sharded_cluster_sts(self):
        sts0 = self.appsv1.read_namespaced_stateful_set("sh001-pv-0", self.namespace)
        assert sts0

    def test_config_sts(self):
        config = self.appsv1.read_namespaced_stateful_set(
            "sh001-pv-config", self.namespace
        )
        assert config

    def test_mongos_sts(self):
        mongos = self.appsv1.read_namespaced_stateful_set(
            "sh001-pv-mongos", self.namespace
        )
        assert mongos

    def test_mongod_sharded_cluster_service(self):
        svc0 = self.corev1.read_namespaced_service("sh001-pv-sh", self.namespace)
        assert svc0

    def test_shard0_was_configured(self):
        hosts = [
            "sh001-pv-0-{}.sh001-pv-sh.{}.svc.cluster.local:27017".format(
                i, self.namespace
            )
            for i in range(3)
        ]

        primary, secondaries = self.wait_for_rs_is_ready(hosts)

        assert primary is not None
        assert len(secondaries) == 2

    def test_pvc_are_bound(self):
        pvc_shards = ["data-sh001-pv-0-{}".format(x) for x in range(3)]
        for pvc_name in pvc_shards:
            pvc = self.corev1.read_namespaced_persistent_volume_claim(
                pvc_name, self.namespace
            )
            assert pvc.status.phase == "Bound"
            assert pvc.spec.resources.requests["storage"] == "1G"

        pvc_config = ["data-sh001-pv-config-{}".format(x) for x in range(3)]
        for pvc_name in pvc_config:
            pvc = self.corev1.read_namespaced_persistent_volume_claim(
                pvc_name, self.namespace
            )
            assert pvc.status.phase == "Bound"
            assert pvc.spec.resources.requests["storage"] == "1G"

    def test_mongos_are_reachable(self):
        self.check_mongos_is_ready(
            "sh001-pv-0-0.sh001-pv-sh.{}.svc.cluster.local:27017".format(self.namespace)
        )
        self.check_mongos_is_ready(
            "sh001-pv-0-1.sh001-pv-sh.{}.svc.cluster.local:27017".format(self.namespace)
        )


@pytest.mark.sharded_cluster_pv
class TestShardedClusterDeletion(KubernetesTester):
    """
    name: Sharded Cluster Deletion with PV
    description: |
      Removes a Sharded Cluster with PV
    delete:
      file: fixtures/sharded-cluster-pv.yaml
      wait_for: 30
    """

    def test_sharded_cluster_doesnt_exist(self):
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set("sh001-pv-0", self.namespace)

    def test_service_does_not_exist(self):
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service("sh001-pv-sh", self.namespace)

    def test_om_sharded_cluster_is_deleted(self):
        config = self.get_automation_config()
        assert len(config["sharding"]) == 0

    def test_om_processes_are_deleted(self):
        config = self.get_automation_config()
        assert len(config["processes"]) == 0
