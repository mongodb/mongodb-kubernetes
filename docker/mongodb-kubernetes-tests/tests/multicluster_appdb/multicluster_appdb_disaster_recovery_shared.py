from typing import Optional

import kubernetes
import kubernetes.client
from kubetester import (
    delete_statefulset,
    get_statefulset,
    read_configmap,
    try_load,
    update_configmap,
)
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from tests.conftest import create_appdb_certs, get_member_cluster_api_client
from tests.multicluster.conftest import cluster_spec_list

FAILED_MEMBER_CLUSTER_NAME = "kind-e2e-cluster-3"
OM_MEMBER_CLUSTER_NAME = "kind-e2e-cluster-1"


class MultiClusterAppDBDisasterRecovery:
    @staticmethod
    def create_ops_manager(
        namespace: str,
        custom_version: str,
        custom_appdb_version: str,
        multi_cluster_issuer_ca_configmap: str,
        central_cluster_client: kubernetes.client.ApiClient,
        member_counts: list,
    ) -> MongoDBOpsManager:
        resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
            yaml_fixture("multicluster_appdb_om.yaml"), namespace=namespace
        )

        if try_load(resource):
            return resource

        resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
        resource["spec"]["version"] = custom_version

        resource.allow_mdb_rc_versions()
        resource.create_admin_secret(api_client=central_cluster_client)

        resource["spec"]["backup"] = {"enabled": False}
        resource["spec"]["applicationDatabase"] = {
            "topology": "MultiCluster",
            "clusterSpecList": cluster_spec_list(["kind-e2e-cluster-2", FAILED_MEMBER_CLUSTER_NAME], member_counts),
            "version": custom_appdb_version,
            "agent": {"logLevel": "DEBUG"},
            "security": {
                "certsSecretPrefix": "prefix",
                "tls": {"ca": multi_cluster_issuer_ca_configmap},
            },
        }

        return resource

    @staticmethod
    def create_appdb_certs_secret(
        namespace: str,
        multi_cluster_issuer: str,
        ops_manager: MongoDBOpsManager,
    ):
        return create_appdb_certs(
            namespace,
            multi_cluster_issuer,
            ops_manager.name + "-db",
            cluster_index_with_members=[(0, 5), (1, 5), (2, 5)],
            cert_prefix="prefix",
        )

    @staticmethod
    def create_config_version():
        class ConfigVersion:
            version = 0

        return ConfigVersion()

    @staticmethod
    def test_patch_central_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
        corev1 = kubernetes.client.CoreV1Api(api_client=central_cluster_client)
        ns = corev1.read_namespace(namespace)
        ns.metadata.labels["istio-injection"] = "enabled"
        corev1.patch_namespace(namespace, ns)

    @staticmethod
    def test_create_om(ops_manager: MongoDBOpsManager, appdb_certs_secret: str, config_version):
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

        config_version.version = ops_manager.get_automation_config_tester().automation_config["version"]

    @staticmethod
    def test_create_om_majority_down(ops_manager: MongoDBOpsManager, appdb_certs_secret: str, config_version):
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

        config_version.version = ops_manager.get_automation_config_tester().automation_config["version"]

    @staticmethod
    def test_remove_cluster_from_operator_member_list_to_simulate_it_is_unhealthy(
        namespace, central_cluster_client: kubernetes.client.ApiClient
    ):
        member_list_cm = read_configmap(
            namespace,
            "mongodb-enterprise-operator-member-list",
            api_client=central_cluster_client,
        )
        if FAILED_MEMBER_CLUSTER_NAME in member_list_cm:
            member_list_cm.pop(FAILED_MEMBER_CLUSTER_NAME)

        update_configmap(
            namespace,
            "mongodb-enterprise-operator-member-list",
            member_list_cm,
            api_client=central_cluster_client,
        )

    @staticmethod
    def test_delete_om_and_appdb_statefulset_in_failed_cluster(
        ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
    ):
        appdb_sts_name = f"{ops_manager.name}-db-1"
        try:
            delete_statefulset(
                ops_manager.namespace,
                ops_manager.name,
                propagation_policy="Background",
                api_client=central_cluster_client,
            )
        except kubernetes.client.ApiException as e:
            if e.status != 404:
                raise e

        try:
            delete_statefulset(
                ops_manager.namespace,
                appdb_sts_name,
                propagation_policy="Background",
                api_client=get_member_cluster_api_client(FAILED_MEMBER_CLUSTER_NAME),
            )
        except kubernetes.client.ApiException as e:
            if e.status != 404:
                raise e

        def statefulset_is_deleted(namespace: str, name: str, api_client=Optional[kubernetes.client.ApiClient]):
            try:
                get_statefulset(namespace, name, api_client=api_client)
                return False
            except kubernetes.client.ApiException as e:
                if e.status == 404:
                    return True
                else:
                    raise e

        run_periodically(
            lambda: statefulset_is_deleted(
                ops_manager.namespace,
                ops_manager.name,
                api_client=get_member_cluster_api_client(OM_MEMBER_CLUSTER_NAME),
            ),
            timeout=120,
        )
        run_periodically(
            lambda: statefulset_is_deleted(
                ops_manager.namespace,
                appdb_sts_name,
                api_client=get_member_cluster_api_client(FAILED_MEMBER_CLUSTER_NAME),
            ),
            timeout=120,
        )

    @staticmethod
    def test_delete_om_and_appdb_statefulset_in_failed_cluster_majority_down(
        ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
    ):
        appdb_sts_name = f"{ops_manager.name}-db-1"
        try:
            delete_statefulset(
                ops_manager.namespace,
                ops_manager.name,
                propagation_policy="Background",
                api_client=central_cluster_client,
            )
        except kubernetes.client.ApiException as e:
            if e.status != 404:
                raise e

        try:
            delete_statefulset(
                ops_manager.namespace,
                appdb_sts_name,
                propagation_policy="Background",
                api_client=get_member_cluster_api_client(FAILED_MEMBER_CLUSTER_NAME),
            )
        except kubernetes.client.ApiException as e:
            if e.status != 404:
                raise e

        def statefulset_is_deleted(namespace: str, name: str, api_client=Optional[kubernetes.client.ApiClient]):
            try:
                get_statefulset(namespace, name, api_client=api_client)
                return False
            except kubernetes.client.ApiException as e:
                if e.status == 404:
                    return True
                else:
                    raise e

        run_periodically(
            lambda: statefulset_is_deleted(
                ops_manager.namespace,
                ops_manager.name,
                api_client=get_member_cluster_api_client(OM_MEMBER_CLUSTER_NAME),
            ),
            timeout=120,
        )
        run_periodically(
            lambda: statefulset_is_deleted(
                ops_manager.namespace,
                appdb_sts_name,
                api_client=get_member_cluster_api_client(FAILED_MEMBER_CLUSTER_NAME),
            ),
            timeout=120,
        )

    @staticmethod
    def test_appdb_is_stable_and_om_is_recreated(ops_manager: MongoDBOpsManager, config_version):
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

        current_ac_version = ops_manager.get_automation_config_tester().automation_config["version"]
        assert current_ac_version == config_version.version

    @staticmethod
    def test_appdb_is_stable_and_om_is_recreated_majority_down(ops_manager: MongoDBOpsManager, config_version):
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

        current_ac_version = ops_manager.get_automation_config_tester().automation_config["version"]
        assert current_ac_version == config_version.version

    @staticmethod
    def test_add_appdb_member_to_om_cluster(ops_manager: MongoDBOpsManager, config_version):
        ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
            ["kind-e2e-cluster-2", FAILED_MEMBER_CLUSTER_NAME, OM_MEMBER_CLUSTER_NAME],
            [3, 2, 1],
        )
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

        current_ac_version = ops_manager.get_automation_config_tester().automation_config["version"]
        assert current_ac_version == config_version.version + 1

        replica_set_members = ops_manager.get_automation_config_tester().get_replica_set_members(f"{ops_manager.name}-db")
        assert len(replica_set_members) == 3 + 2 + 1

        config_version.version = current_ac_version

    @staticmethod
    def test_add_appdb_member_to_om_cluster_force_reconfig(ops_manager: MongoDBOpsManager, config_version):
        ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
            ["kind-e2e-cluster-2", FAILED_MEMBER_CLUSTER_NAME, OM_MEMBER_CLUSTER_NAME],
            [3, 2, 1],
        )
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Pending)

        ops_manager.reload()
        ops_manager["metadata"]["annotations"].update({"mongodb.com/v1.forceReconfigure": "true"})
        ops_manager.update()

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

        replica_set_members = ops_manager.get_automation_config_tester().get_replica_set_members(f"{ops_manager.name}-db")
        assert len(replica_set_members) == 3 + 2 + 1

        config_version.version = ops_manager.get_automation_config_tester().automation_config["version"]

    @staticmethod
    def test_remove_failed_member_cluster_has_been_scaled_down(ops_manager: MongoDBOpsManager, config_version):
        ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
            ["kind-e2e-cluster-2", OM_MEMBER_CLUSTER_NAME], [3, 1]
        )
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

        current_ac_version = ops_manager.get_automation_config_tester().automation_config["version"]
        assert current_ac_version == config_version.version + 2

        replica_set_members = ops_manager.get_automation_config_tester().get_replica_set_members(f"{ops_manager.name}-db")
        assert len(replica_set_members) == 3 + 1

    @staticmethod
    def test_remove_failed_member_cluster_has_been_scaled_down_majority_down(ops_manager: MongoDBOpsManager, config_version):
        ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
            ["kind-e2e-cluster-2", OM_MEMBER_CLUSTER_NAME], [3, 1]
        )
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

        current_ac_version = ops_manager.get_automation_config_tester().automation_config["version"]
        assert current_ac_version == config_version.version + 2

        replica_set_members = ops_manager.get_automation_config_tester().get_replica_set_members(f"{ops_manager.name}-db")
        assert len(replica_set_members) == 3 + 1