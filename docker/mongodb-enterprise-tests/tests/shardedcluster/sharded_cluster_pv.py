import kubernetes
import pytest
from kubetester import MongoDB, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically, skip_if_multi_cluster
from kubetester.mongodb import Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
)


@fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-pv.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    return resource.update()


@mark.e2e_sharded_cluster_pv
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_pv
class TestShardedClusterCreation:
    custom_labels = {"label1": "val1", "label2": "val2"}

    def test_sharded_cluster_created(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def check_sts_labels(self, sts):
        sts_labels = sts.metadata.labels
        for k in self.custom_labels:
            assert k in sts_labels and sts_labels[k] == self.custom_labels[k]

    def check_pvc_labels(self, pvc):
        pvc_labels = pvc.metadata.labels
        for k in self.custom_labels:
            assert k in pvc_labels and pvc_labels[k] == self.custom_labels[k]

    def test_sharded_cluster_sts(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            cluster_idx = cluster_member_client.cluster_index

            shard_sts_name = sc.shard_statefulset_name(0, cluster_idx)
            shard_sts = cluster_member_client.read_namespaced_stateful_set(shard_sts_name, sc.namespace)
            assert shard_sts
            self.check_sts_labels(shard_sts)

    def test_config_sts(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            cluster_idx = cluster_member_client.cluster_index

            config_srv_sts_name = sc.config_srv_statefulset_name(cluster_idx)
            config_srv_sts = cluster_member_client.read_namespaced_stateful_set(config_srv_sts_name, sc.namespace)
            assert config_srv_sts
            self.check_sts_labels(config_srv_sts)

    def test_mongos_sts(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            cluster_idx = cluster_member_client.cluster_index

            mongos_sts_name = sc.config_srv_statefulset_name(cluster_idx)
            mongos_sts = cluster_member_client.read_namespaced_stateful_set(mongos_sts_name, sc.namespace)
            assert mongos_sts
            self.check_sts_labels(mongos_sts)

    def test_mongod_sharded_cluster_service(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            shard_service_name = sc.shard_service_name()
            shard_service = cluster_member_client.read_namespaced_service(shard_service_name, sc.namespace)
            assert shard_service

    def test_shard0_was_configured(self, sc: MongoDB):
        hosts = []
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            for member_idx in range(sc.shard_members_in_cluster(cluster_member_client.cluster_name)):
                hostname = sc.shard_hostname(0, member_idx, cluster_member_client.cluster_index)
                hosts.append(hostname)

        primary, secondaries = KubernetesTester.wait_for_rs_is_ready(hosts)

        assert primary is not None
        assert len(secondaries) == 2

    def test_pvc_are_bound(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            cluster_idx = cluster_member_client.cluster_index

            for member_idx in range(sc.shard_members_in_cluster(cluster_member_client.cluster_name)):
                pvc_name = sc.shard_pvc_name(0, member_idx, cluster_idx)
                pvc = cluster_member_client.read_namespaced_persistent_volume_claim(pvc_name, sc.namespace)
                assert pvc.status.phase == "Bound"
                assert pvc.spec.resources.requests["storage"] == "1G"
                self.check_pvc_labels(pvc)

            for member_idx in range(sc.config_srv_members_in_cluster(cluster_member_client.cluster_name)):
                pvc_name = sc.config_srv_pvc_name(member_idx, cluster_idx)
                pvc = cluster_member_client.read_namespaced_persistent_volume_claim(pvc_name, sc.namespace)
                assert pvc.status.phase == "Bound"
                assert pvc.spec.resources.requests["storage"] == "1G"
                self.check_pvc_labels(pvc)

    def test_mongos_are_reachable(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            for member_idx in range(sc.mongos_members_in_cluster(cluster_member_client.cluster_name)):
                service_name = sc.mongos_service_name(member_idx, cluster_member_client.cluster_index)
                tester = sc.tester(service_names=[service_name])
                tester.assert_connectivity()


@mark.e2e_sharded_cluster_pv
@skip_if_multi_cluster  # Currently removing Kubernetes resources in multi-cluster sharded is not implemented
class TestShardedClusterDeletion:
    def test_delete_sharded_cluster_resource(self, sc: MongoDB):
        sc.delete()

        def resource_is_deleted() -> bool:
            try:
                sc.load()
                return False
            except kubernetes.client.ApiException as e:
                return e.status == 404

        run_periodically(resource_is_deleted, timeout=240)

    def test_sharded_cluster_doesnt_exist(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            sts = cluster_member_client.list_namespaced_stateful_set(sc.namespace)
            assert len(sts.items) == 0

    def test_service_does_not_exist(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            with pytest.raises(kubernetes.client.ApiException) as api_exception:
                cluster_member_client.read_namespaced_service(sc.shard_service_name(), sc.namespace)
            assert api_exception.value.status == 404
