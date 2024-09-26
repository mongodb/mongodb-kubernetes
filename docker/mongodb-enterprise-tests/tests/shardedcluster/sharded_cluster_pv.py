import time

import pytest
from kubernetes import client
from kubetester import MongoDB, create_or_update, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongotester import ShardedClusterTester


@pytest.fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-pv.yaml"),
        namespace=namespace,
    )
    try_load(resource)
    return resource


@pytest.mark.e2e_sharded_cluster_pv
class TestShardedClusterCreation(KubernetesTester):
    custom_labels = {"label1": "val1", "label2": "val2"}

    def test_sharded_cluster_created(self, sharded_cluster: MongoDB):
        create_or_update(sharded_cluster)
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=360)

    def check_sts_labels(self, sts):
        sts_labels = sts.metadata.labels
        for k in self.custom_labels:
            assert k in sts_labels and sts_labels[k] == self.custom_labels[k]

    def check_pvc_labels(self, pvc):
        pvc_labels = pvc.metadata.labels
        for k in self.custom_labels:
            assert k in pvc_labels and pvc_labels[k] == self.custom_labels[k]

    def test_sharded_cluster_sts(self):
        sts0 = self.appsv1.read_namespaced_stateful_set("sh001-pv-0", self.namespace)
        assert sts0
        self.check_sts_labels(sts0)

    def test_config_sts(self):
        config = self.appsv1.read_namespaced_stateful_set("sh001-pv-config", self.namespace)
        assert config
        self.check_sts_labels(config)

    def test_mongos_sts(self):
        mongos = self.appsv1.read_namespaced_stateful_set("sh001-pv-mongos", self.namespace)
        assert mongos
        self.check_sts_labels(mongos)

    def test_mongod_sharded_cluster_service(self):
        svc0 = self.corev1.read_namespaced_service("sh001-pv-sh", self.namespace)
        assert svc0

    def test_shard0_was_configured(self):
        hosts = ["sh001-pv-0-{}.sh001-pv-sh.{}.svc.cluster.local:27017".format(i, self.namespace) for i in range(3)]

        primary, secondaries = self.wait_for_rs_is_ready(hosts)

        assert primary is not None
        assert len(secondaries) == 2

    def test_pvc_are_bound(self):
        pvc_shards = ["data-sh001-pv-0-{}".format(x) for x in range(3)]
        for pvc_name in pvc_shards:
            pvc = self.corev1.read_namespaced_persistent_volume_claim(pvc_name, self.namespace)
            assert pvc.status.phase == "Bound"
            assert pvc.spec.resources.requests["storage"] == "1G"
            self.check_pvc_labels(pvc)

        pvc_config = ["data-sh001-pv-config-{}".format(x) for x in range(3)]
        for pvc_name in pvc_config:
            pvc = self.corev1.read_namespaced_persistent_volume_claim(pvc_name, self.namespace)
            assert pvc.status.phase == "Bound"
            assert pvc.spec.resources.requests["storage"] == "1G"
            self.check_pvc_labels(pvc)

    def test_mongos_are_reachable(self):
        ShardedClusterTester("sh001-pv", 2)


@pytest.mark.e2e_sharded_cluster_pv
class TestShardedClusterDeletion(KubernetesTester):
    def test_sharded_cluster_delete(self, sharded_cluster: MongoDB):
        sharded_cluster.delete()

    def test_sharded_cluster_doesnt_exist(self):
        """The StatefulSet must be removed by Kubernetes as soon as the MongoDB resource is removed.
        Note, that this may lag sometimes (caching or whatever?) and it's more safe to wait a bit"""
        time.sleep(15)
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set("sh001-pv-0", self.namespace)

    def test_service_does_not_exist(self):
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service("sh001-pv-sh", self.namespace)
