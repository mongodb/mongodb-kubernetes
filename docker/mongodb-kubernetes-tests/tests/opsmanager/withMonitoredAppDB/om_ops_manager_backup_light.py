from typing import Dict, Optional

import jsonpatch
import kubernetes.client
from kubernetes import client
from kubetester import try_load, wait_until
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_central_cluster_client, get_member_cluster_api_client, is_multi_cluster
from tests.opsmanager.om_ops_manager_backup import (
    HEAD_PATH,
    OPLOG_RS_NAME,
    S3_SECRET_NAME,
    create_aws_secret,
    create_s3_bucket,
    new_om_data_store,
)
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

DEFAULT_APPDB_USER_NAME = "mongodb-ops-manager"

"""
This test checks the backup if no separate S3 Metadata database is created and AppDB is used for this.
Note, that it doesn't check for mongodb backup as it's done in 'e2e_om_ops_manager_backup_restore'"
"""


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, bucket_prefix="test-s3-bucket-")


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_bucket: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_light.yaml"), namespace=namespace
    )

    if try_load(resource):
        return resource

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace, custom_mdb_version) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")

    resource.set_version(custom_mdb_version)

    try_load(resource)

    return resource


def service_exists(service_name: str, namespace: str, api_client: Optional[kubernetes.client.ApiClient] = None) -> bool:
    try:
        client.CoreV1Api(api_client=api_client).read_namespaced_service(service_name, namespace)
    except client.rest.ApiException:
        return False
    return True


@mark.e2e_om_ops_manager_backup_light
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        """creates a s3 bucket and an OM resource, the S3 configs get created using AppDB. Oplog store is still required."""
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Oplog Store configuration is required for backup",
            timeout=600,
        )

    def test_oplog_mdb_created(
        self,
        oplog_replica_set: MongoDB,
    ):
        oplog_replica_set.update()
        oplog_replica_set.assert_reaches_phase(Phase.Running)

    def test_add_oplog_config(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"] = [
            {"name": "oplog1", "mongodbResourceRef": {"name": "my-mongodb-oplog"}}
        ]
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=500,
            ignore_errors=True,
        )

    @skip_if_local
    def test_om(
        self,
        ops_manager: MongoDBOpsManager,
        s3_bucket: str,
        aws_s3_client: AwsS3Client,
        oplog_replica_set: MongoDB,
    ):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        for pod_fqdn in ops_manager.backup_daemon_pods_headless_fqdns():
            om_tester.assert_daemon_enabled(pod_fqdn, HEAD_PATH)
        om_tester.assert_oplog_stores([new_om_data_store(oplog_replica_set, "oplog1")])

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.assert_appdb_monitoring_group_was_created()

        # TODO uncomment when CLOUDP-70468 is fixed and AppDB supports scram-sha-256
        # making sure the s3 config pushed to OM references the appdb
        # appdb_replica_set = ops_manager.get_appdb_resource()
        # appdb_password = KubernetesTester.read_secret(
        #     ops_manager.namespace, ops_manager.app_db_password_secret_name()
        # )["password"]
        # om_tester.assert_s3_stores(
        #     [
        #         new_om_s3_store(
        #             appdb_replica_set,
        #             "s3Store1",
        #             s3_bucket,
        #             aws_s3_client,
        #             user_name=DEFAULT_APPDB_USER_NAME,
        #             password=appdb_password,
        #         )
        #     ]
        # )

    def test_enable_external_connectivity(self, ops_manager: MongoDBOpsManager, namespace: str):
        ops_manager.load()
        ops_manager["spec"]["externalConnectivity"] = {"type": "LoadBalancer"}
        ops_manager.update()

        for member_cluster_name, _ in ops_manager.get_om_sts_names_in_member_clusters():
            om_external_service_name = f"{ops_manager.name}-svc-ext"
            wait_until(
                lambda: service_exists(
                    om_external_service_name, namespace, api_client=get_member_cluster_api_client(member_cluster_name)
                ),
                timeout=90,
                sleep_time=5,
            )

            service = client.CoreV1Api(
                api_client=get_member_cluster_api_client(member_cluster_name)
            ).read_namespaced_service(om_external_service_name, namespace)

            # Tests that the service is created with both externalConnectivity and backup
            # and that it contains the correct ports
            assert service.spec.type == "LoadBalancer"
            assert len(service.spec.ports) == 2
            assert service.spec.ports[0].port == 8080
            assert service.spec.ports[1].port == 25999

    def test_disable_external_connectivity(self, ops_manager: MongoDBOpsManager, namespace: str):
        # We don't have a nice way to delete fields from a resource specification
        # in our test env, so we need to achieve it with specific uses of patches
        body = {"op": "remove", "path": "/spec/externalConnectivity"}
        patch = jsonpatch.JsonPatch([body])

        om = client.CustomObjectsApi().get_namespaced_custom_object(
            ops_manager.group,
            ops_manager.version,
            namespace,
            ops_manager.plural,
            ops_manager.name,
        )
        client.CustomObjectsApi().replace_namespaced_custom_object(
            ops_manager.group,
            ops_manager.version,
            namespace,
            ops_manager.plural,
            ops_manager.name,
            jsonpatch.apply_patch(om, patch),
        )

        for member_cluster_name, _ in ops_manager.get_om_sts_names_in_member_clusters():
            om_external_service_name = f"{ops_manager.name}-svc-ext"

            wait_until(
                lambda: not (
                    service_exists(
                        om_external_service_name,
                        namespace,
                        api_client=get_member_cluster_api_client(member_cluster_name),
                    )
                ),
                timeout=90,
                sleep_time=5,
            )


@mark.e2e_om_ops_manager_backup_light
def test_backup_statefulset_remains_after_disabling_backup(
    ops_manager: MongoDBOpsManager,
):
    ops_manager.load()
    ops_manager["spec"]["backup"]["enabled"] = False
    ops_manager.update()

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=200)

    # the backup statefulset should still exist even after we disable backup
    ops_manager.read_backup_statefulset()


def check_sts_labels(sts, labels: Dict[str, str]):
    sts_labels = sts.metadata.labels

    for k in labels:
        assert k in sts_labels and sts_labels[k] == labels[k]


@mark.e2e_om_ops_manager_backup_light
def test_labels_on_om_and_backup_daemon_and_appdb_sts(ops_manager: MongoDBOpsManager, namespace: str):
    labels = {"label1": "val1", "label2": "val2"}

    check_sts_labels(ops_manager.read_statefulset(), labels)
    check_sts_labels(ops_manager.read_backup_statefulset(), labels)
    check_sts_labels(ops_manager.read_appdb_statefulset(), labels)


def check_pvc_labels(pvc_name: str, labels: Dict[str, str], namespace: str, api_client: kubernetes.client.ApiClient):
    pvc = client.CoreV1Api(api_client=api_client).read_namespaced_persistent_volume_claim(pvc_name, namespace)
    pvc_labels = pvc.metadata.labels

    for k in labels:
        assert k in pvc_labels and pvc_labels[k] == labels[k]


@mark.e2e_om_ops_manager_backup_light
def test_labels_on_backup_daemon_and_appdb_pvc(ops_manager: MongoDBOpsManager, namespace: str):
    labels = {"label1": "val1", "label2": "val2"}

    appdb_pvc_name = "data-{}-0".format(ops_manager.read_appdb_statefulset().metadata.name)

    member_cluster_name = ops_manager.pick_one_appdb_member_cluster_name()
    check_pvc_labels(appdb_pvc_name, labels, namespace, api_client=get_member_cluster_api_client(member_cluster_name))
    backupdaemon_pvc_name = "head-{}-0".format(ops_manager.read_backup_statefulset().metadata.name)

    check_pvc_labels(backupdaemon_pvc_name, labels, namespace, api_client=get_central_cluster_client())
