"""
Backup that is enabled outside of the operator for a MongoDBMultiCluster whose credentials are a
project-scoped (GROUP_OWNER) Ops Manager API key.

Flow:
 1. a MongoDBMultiCluster is deployed without `spec.backup` (OM is prepared for backup, but backup is off),
 2. backup is started the way the Ops Manager UI does it (backupConfigs API with the project key),
 3. `spec.backup` with a snapshot schedule and `assignmentLabels` is added to the CR: the operator needs
    GLOBAL_READ_ONLY/GLOBAL_BACKUP_ADMIN for the labels, so the reconciliation fails with 401,
 4. `assignmentLabels` is removed from the CR: the resource gets deployed again, the snapshot schedule is applied
    and `status.backup` is expected to report the state of the running backup.
"""

import random
import string
from typing import Dict, List, Optional

import kubernetes
import kubernetes.client
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    get_default_storage_class,
    read_service,
    try_load,
)
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger

# The Ops Manager with backup, oplog and blockstore is set up by the fixtures of the multi-cluster backup test.
from tests.multicluster.multi_cluster_backup_restore import (  # noqa: F401
    blockstore_replica_set,
    blockstore_user,
    oplog_replica_set,
    oplog_user,
    ops_manager,
    ops_manager_certs,
)

logger = test_logger.get_test_logger(__name__)

# Unique per run so that leftovers of a previous (possibly failed) run in the shared OM don't collide.
RUN_SUFFIX = "".join(random.choices(string.ascii_lowercase + string.digits, k=6))
RESOURCE_NAME = f"mdb-scoped-{RUN_SUFFIX}"
ASSIGNMENT_LABELS = ["tenant-label"]

SNAPSHOT_SCHEDULE = {
    "snapshotIntervalHours": 12,
    "snapshotRetentionDays": 3,
    "dailySnapshotRetentionDays": 5,
    "weeklySnapshotRetentionWeeks": 4,
    "monthlySnapshotRetentionMonths": 5,
}


@fixture(scope="module")
def admin_om_tester(ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient) -> OMTester:
    ops_manager.load()
    return ops_manager.get_om_tester(api_client=central_cluster_client)


@fixture(scope="module")
def om_service_in_member_clusters(
    ops_manager: MongoDBOpsManager,
    namespace: str,
    central_cluster_name: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
) -> str:
    """Makes OM's in-cluster service name resolve in the member clusters, so the very same baseUrl works for the
    operator (central cluster, or a local operator through the cluster proxy) and for the agents.
    Each member cluster gets a selector-less Service with Endpoints pointing at OM's LoadBalancer IP."""
    ops_manager.load()
    om_service_name = ops_manager.svc_name()
    ip = (
        read_service(namespace, ops_manager.external_svc_name(), api_client=central_cluster_client)
        .status.load_balancer.ingress[0]
        .ip
    )
    port = 8443
    for member in member_cluster_clients:
        if member.cluster_name == central_cluster_name:
            continue
        service = client.V1Service(
            metadata=client.V1ObjectMeta(name=om_service_name, namespace=namespace),
            spec=client.V1ServiceSpec(ports=[client.V1ServicePort(name="https", port=port, protocol="TCP")]),
        )
        endpoints = client.V1Endpoints(
            metadata=client.V1ObjectMeta(name=om_service_name, namespace=namespace),
            subsets=[
                client.V1EndpointSubset(
                    addresses=[client.V1EndpointAddress(ip=ip)],
                    ports=[client.CoreV1EndpointPort(name="https", port=port, protocol="TCP")],
                )
            ],
        )
        core_v1 = member.core_v1_api()
        for create in (
            lambda: core_v1.create_namespaced_service(namespace, service),
            lambda: core_v1.create_namespaced_endpoints(namespace, endpoints),
        ):
            try:
                create()
            except ApiException as e:
                if e.status != 409:
                    raise
    return ops_manager.om_status().get_url()


@fixture(scope="module")
def scoped_credentials(
    namespace: str,
    admin_om_tester: OMTester,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_issuer_ca_configmap: str,
    om_service_in_member_clusters: str,
) -> Dict:
    """Organization, project and a GROUP_OWNER (project-scoped) API key, wired into a configmap and a secret
    that a MongoDBMultiCluster can use. No global role is granted to that key."""
    org = admin_om_tester.om_request("post", "/orgs", json_object={"name": RESOURCE_NAME}).json()
    group = admin_om_tester.om_request(
        "post", "/groups", json_object={"name": RESOURCE_NAME, "orgId": org["id"]}
    ).json()
    key = admin_om_tester.om_request(
        "post",
        f"/groups/{group['id']}/apiKeys",
        json_object={"desc": "project scoped key for backup test", "roles": ["GROUP_OWNER"]},
    ).json()
    admin_om_tester.om_request(
        "post",
        f"/orgs/{org['id']}/apiKeys/{key['id']}/accessList",
        json_object=[{"cidrBlock": "0.0.0.0/1"}, {"cidrBlock": "128.0.0.0/1"}],
    )

    secret_name = f"{RESOURCE_NAME}-credentials"
    create_or_update_secret(
        namespace,
        secret_name,
        {"publicKey": key["publicKey"], "privateKey": key["privateKey"]},
        api_client=central_cluster_client,
    )
    config_name = f"{RESOURCE_NAME}-config"
    create_or_update_configmap(
        namespace,
        config_name,
        {
            "baseUrl": om_service_in_member_clusters,
            "projectName": RESOURCE_NAME,
            "orgId": org["id"],
            "sslMMSCAConfigMap": multi_cluster_issuer_ca_configmap,
        },
        api_client=central_cluster_client,
    )

    return {
        "org_id": org["id"],
        "group_id": group["id"],
        "secret_name": secret_name,
        "config_name": config_name,
        # talks to OM exactly like the UI user / operator would, with the scoped key only
        "tester": OMTester(
            OMContext(
                admin_om_tester.context.base_url,
                key["publicKey"],
                key["privateKey"],
                project_name=RESOURCE_NAME,
                project_id=group["id"],
                org_id=org["id"],
            )
        ),
    }


@fixture(scope="module")
def mongodb_multi(
    namespace: str,
    member_cluster_names: List[str],
    central_cluster_client: kubernetes.client.ApiClient,
    custom_mdb_version: str,
    scoped_credentials: Dict,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["opsManager"]["configMapRef"]["name"] = scoped_credentials["config_name"]
    resource["spec"]["credentials"] = scoped_credentials["secret_name"]
    resource["spec"]["clusterSpecList"] = [
        {"clusterName": member_cluster_names[0], "members": 1},
        {"clusterName": member_cluster_names[1], "members": 1},
        {"clusterName": member_cluster_names[2], "members": 1},
    ]
    try_load(resource)
    return resource


def backup_config_status(tester: OMTester) -> Optional[str]:
    configs = tester.api_read_backup_configs()
    return configs[0]["statusName"] if configs else None


@mark.e2e_multi_cluster_backup_scoped_credentials
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_multi_cluster_backup_scoped_credentials
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


@mark.e2e_multi_cluster_backup_scoped_credentials
class TestBackupEnabledOutsideOfOperator:
    def test_deploy_mongodb_multi_without_backup(self, mongodb_multi: MongoDBMulti):
        assert "backup" not in mongodb_multi["spec"]
        mongodb_multi.update()
        mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=1200)

    def test_backup_is_not_started_yet(self, scoped_credentials: Dict):
        tester: OMTester = scoped_credentials["tester"]

        def backup_config_exists() -> bool:
            return len(tester.api_read_backup_configs()) > 0

        KubernetesTester.wait_until(backup_config_exists, timeout=300)
        assert backup_config_status(tester) == "INACTIVE"

    def test_enable_backup_like_the_ui(self, scoped_credentials: Dict):
        """Starts the backup the way the Ops Manager UI does, with the project scoped key."""
        tester: OMTester = scoped_credentials["tester"]
        cluster_id = tester.get_backup_cluster_id()
        tester.om_request(
            "patch",
            f"/groups/{scoped_credentials['group_id']}/backupConfigs/{cluster_id}",
            json_object={"statusName": "STARTED"},
        )

        KubernetesTester.wait_until(lambda: backup_config_status(tester) == "STARTED", timeout=300)

    def test_backup_works(self, scoped_credentials: Dict):
        scoped_credentials["tester"].wait_until_backup_snapshots_are_ready(expected_count=1)

    def test_resource_unaffected_by_manual_backup(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=300)


@mark.e2e_multi_cluster_backup_scoped_credentials
class TestConfiguringBackupInCrWithScopedKey:
    def test_snapshot_schedule_and_labels_in_cr_fail(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.load()
        mongodb_multi.configure_backup(mode="enabled")
        mongodb_multi["spec"]["backup"]["snapshotSchedule"] = SNAPSHOT_SCHEDULE
        mongodb_multi["spec"]["backup"]["assignmentLabels"] = ASSIGNMENT_LABELS
        mongodb_multi.update()

        # GET/PUT /admin/backup/groups/{id} need GLOBAL_READ_ONLY / GLOBAL_BACKUP_ADMIN, a GROUP_OWNER key has neither
        mongodb_multi.assert_reaches_phase(Phase.Failed, timeout=300)
        message = mongodb_multi.get_status_message()
        logger.info(f"CR failed with: {message}")
        assert "Failed to configure backup" in message or "401" in message

    def test_backup_still_running_in_om(self, scoped_credentials: Dict):
        assert backup_config_status(scoped_credentials["tester"]) == "STARTED"

    def test_remove_labels_from_cr(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.load()
        # keys are removed from the merge patch by setting them to None
        mongodb_multi["spec"]["backup"]["assignmentLabels"] = None
        mongodb_multi.update()
        mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=600)

    def test_snapshot_schedule_applied_in_om(self, scoped_credentials: Dict):
        schedule = scoped_credentials["tester"].api_read_backup_snapshot_schedule()
        for key, value in SNAPSHOT_SCHEDULE.items():
            assert schedule[key] == value, f"{key}: expected {value}, OM has {schedule.get(key)}"

    def test_backup_still_running_after_labels_removed(self, scoped_credentials: Dict):
        assert backup_config_status(scoped_credentials["tester"]) == "STARTED"

    def test_backup_status_is_reported_in_cr(self, mongodb_multi: MongoDBMulti):
        """Backup is up and running in OM, so the operator is expected to report it in status.backup."""
        mongodb_multi.load()
        logger.info(f"status of the resource: {mongodb_multi['status']}")
        mongodb_multi.assert_backup_reaches_status("STARTED", timeout=120)
