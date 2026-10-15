"""
Backup that is enabled outside of the operator, for example in the Ops Manager UI, for a MongoDBMultiCluster.
Once `spec.backup` is added to the CR, `status.backup` is expected to report the state of the running backup.
"""

import random
import string
from typing import List

import kubernetes
import kubernetes.client
from kubetester import create_or_update_configmap, get_default_storage_class, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.omtester import OMTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.ops_manager.backup_outside_operator import (
    backup_config_status,
    start_backup_like_the_ui,
    wait_for_backup_config,
)
from tests.conftest import local_operator

# The Ops Manager with backup, oplog and blockstore is set up by the fixtures of the multi-cluster backup test.
from tests.multicluster.multi_cluster_backup_restore import (  # noqa: F401
    blockstore_replica_set,
    blockstore_user,
    expose_om_to_member_clusters,
    interconnected_base_url,
    oplog_replica_set,
    oplog_user,
    ops_manager,
    ops_manager_certs,
)

logger = test_logger.get_test_logger(__name__)

# Unique per run so that leftovers of a previous run in the shared Ops Manager don't collide.
RESOURCE_NAME = "mdb-backup-" + "".join(random.choices(string.ascii_lowercase + string.digits, k=6))


def om_base_url(ops_manager: MongoDBOpsManager) -> str:
    """The address of Ops Manager for the operator, the agents and the test. In Evergreen the operator and Ops Manager
    are in a cluster of their own, so the interconnected address that every cluster resolves is needed. A local
    operator reaches the plain service name through the cluster proxy and cannot resolve the interconnected one."""
    if local_operator():
        url = ops_manager.om_status().get_url()
        assert url is not None, "Ops Manager URL must not be None"
        return url
    return interconnected_base_url(ops_manager)


@fixture(scope="module")
def mongodb_multi(
    namespace: str,
    ops_manager: MongoDBOpsManager,
    member_cluster_names: List[str],
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_issuer_ca_configmap: str,
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace).configure(
        ops_manager, RESOURCE_NAME, api_client=central_cluster_client
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["clusterSpecList"] = [
        {"clusterName": member_cluster_names[0], "members": 1},
        {"clusterName": member_cluster_names[1], "members": 1},
        {"clusterName": member_cluster_names[2], "members": 1},
    ]

    config_name = f"{RESOURCE_NAME}-config"
    data = KubernetesTester.read_configmap(namespace, config_name, api_client=central_cluster_client)
    data["baseUrl"] = om_base_url(ops_manager)
    data["sslMMSCAConfigMap"] = multi_cluster_issuer_ca_configmap
    create_or_update_configmap(namespace, config_name, data, api_client=central_cluster_client)

    try_load(resource)
    return resource


@fixture(scope="module")
def project(ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient) -> OMTester:
    ops_manager.load()
    return ops_manager.get_om_tester(
        project_name=RESOURCE_NAME, api_client=central_cluster_client, base_url=om_base_url(ops_manager)
    )


@mark.e2e_multi_cluster_backup_enabled_outside_operator
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_multi_cluster_backup_enabled_outside_operator
class TestOpsManagerPreparedForBackup:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
        ops_manager["spec"]["backup"]["members"] = 1
        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending, msg_regexp="The MongoDB object .+ doesn't exist", timeout=1800
        )

    def test_backup_databases_created(
        self,
        oplog_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
        oplog_user,
    ):
        oplog_replica_set.update()
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        blockstore_replica_set.update()
        blockstore_replica_set.assert_reaches_phase(Phase.Running)
        oplog_user.update()
        oplog_user.assert_reaches_phase(Phase.Updated)

    def test_om_backup_running(self, ops_manager: MongoDBOpsManager, oplog_user):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"][0]["mongodbUserRef"] = {"name": oplog_user.name}
        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=300, ignore_errors=True)

    def test_expose_om_to_member_clusters(
        self,
        ops_manager: MongoDBOpsManager,
        central_cluster_client: kubernetes.client.ApiClient,
        member_cluster_clients: List[MultiClusterClient],
    ):
        if local_operator():
            return
        expose_om_to_member_clusters(ops_manager, central_cluster_client, member_cluster_clients)
        # changing mms.centralUrl restarts Ops Manager
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900, ignore_errors=True)


@mark.e2e_multi_cluster_backup_enabled_outside_operator
class TestBackupEnabledOutsideOfOperator:
    def test_deploy_mongodb_multi_without_backup(self, mongodb_multi: MongoDBMulti):
        assert "backup" not in mongodb_multi["spec"]
        mongodb_multi.update()
        mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=1200)

    def test_link_to_ops_manager_is_reported_in_status(
        self, mongodb_multi: MongoDBMulti, ops_manager: MongoDBOpsManager, project: OMTester
    ):
        mongodb_multi.load()
        assert mongodb_multi["status"]["link"] == f"{om_base_url(ops_manager)}/v2/{project.context.project_id}"

    def test_backup_is_not_started_yet(self, project: OMTester):
        wait_for_backup_config(project)
        assert backup_config_status(project) == "INACTIVE"

    def test_enable_backup_like_the_ui(self, project: OMTester):
        start_backup_like_the_ui(project)

    def test_resource_unaffected_by_manual_backup(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=300)


@mark.e2e_multi_cluster_backup_enabled_outside_operator
class TestBackupEnabledInCr:
    def test_enable_backup_in_cr(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.load()
        mongodb_multi.configure_backup(mode="enabled")
        mongodb_multi.update()
        mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=600)

    def test_backup_still_running_in_om(self, project: OMTester):
        assert backup_config_status(project) == "STARTED"

    def test_backup_status_is_reported_in_cr(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.load()
        logger.info(f"status of the resource: {mongodb_multi['status']}")
        mongodb_multi.assert_backup_reaches_status("STARTED", timeout=120)
