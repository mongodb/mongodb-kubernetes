import kubernetes
from pytest import fixture, mark

from .multicluster_appdb_disaster_recovery_shared import MultiClusterAppDBDisasterRecovery


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_appdb_disaster_recovery
class TestMultiClusterAppDBDisasterRecoveryNormal:

    @fixture(scope="module")
    def ops_manager(
        self,
        namespace: str,
        custom_version: str,
        custom_appdb_version: str,
        multi_cluster_issuer_ca_configmap: str,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        return MultiClusterAppDBDisasterRecovery.create_ops_manager(
            namespace, custom_version,  custom_appdb_version, multi_cluster_issuer_ca_configmap, central_cluster_client, member_counts=[3, 2]
        )

    @fixture(scope="module")
    def appdb_certs_secret(self, namespace: str, multi_cluster_issuer: str, ops_manager):
        return MultiClusterAppDBDisasterRecovery.create_appdb_certs_secret(namespace, multi_cluster_issuer, ops_manager)

    @fixture(scope="module")
    def config_version(self):
        return MultiClusterAppDBDisasterRecovery.create_config_version()

    def test_patch_central_namespace(self, namespace: str, central_cluster_client: kubernetes.client.ApiClient):
        MultiClusterAppDBDisasterRecovery.test_patch_central_namespace(namespace, central_cluster_client)

    def test_create_om(self, ops_manager, appdb_certs_secret, config_version):
        MultiClusterAppDBDisasterRecovery.test_create_om(ops_manager, appdb_certs_secret, config_version)

    def test_remove_cluster_from_operator_member_list_to_simulate_it_is_unhealthy(
        self, namespace, central_cluster_client: kubernetes.client.ApiClient
    ):
        MultiClusterAppDBDisasterRecovery.test_remove_cluster_from_operator_member_list_to_simulate_it_is_unhealthy(namespace, central_cluster_client)

    def test_delete_om_and_appdb_statefulset_in_failed_cluster(
        self, ops_manager, central_cluster_client: kubernetes.client.ApiClient
    ):
        MultiClusterAppDBDisasterRecovery.test_delete_om_and_appdb_statefulset_in_failed_cluster(ops_manager, central_cluster_client)

    def test_appdb_is_stable_and_om_is_recreated(self, ops_manager, config_version):
        MultiClusterAppDBDisasterRecovery.test_appdb_is_stable_and_om_is_recreated(ops_manager, config_version)

    def test_add_appdb_member_to_om_cluster(self, ops_manager, config_version):
        MultiClusterAppDBDisasterRecovery.test_add_appdb_member_to_om_cluster(ops_manager, config_version)

    def test_remove_failed_member_cluster_has_been_scaled_down(self, ops_manager, config_version):
        MultiClusterAppDBDisasterRecovery.test_remove_failed_member_cluster_has_been_scaled_down(ops_manager, config_version)
